// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/mux"
)

// udpRelay owns listener routes and session flow tables. Its mutex protects
// metadata only; DNS, socket I/O, and tunnel selection happen outside it.
type udpRelay struct {
	s         *Server
	mu        sync.Mutex
	listeners map[string]*udpListener // identity -> active listener
	sockets   map[string]*udpListener // actual address, including draining sockets
	sessions  map[mux.Session]*udpSession
	closed    bool
	unrouted  atomic.Uint64
}

type udpListener struct {
	conn     *net.UDPConn
	id, addr string
	active   bool
	refs     int // active listener plus locally opened flows
	routes   map[netip.AddrPort]*udpFlow
	writeMu  sync.Mutex
	closed   chan struct{}
}

type udpSession struct {
	ss      mux.Session
	d       *mux.DatagramChannel
	metrics *mux.SessionMetrics
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	flows   map[uint16]*udpFlow
	next    uint16
}

type udpFlow struct {
	owner    *udpSession
	id       uint16
	lastSeen time.Time
	listener *udpListener // active flow, replies use this shared socket
	client   netip.AddrPort
	conn     *net.UDPConn // passive flow, connected to the local connect target
}

func newUDPRelay(s *Server) *udpRelay {
	return &udpRelay{s: s, listeners: make(map[string]*udpListener),
		sockets: make(map[string]*udpListener), sessions: make(map[mux.Session]*udpSession)}
}

func (r *udpRelay) attach(ss mux.Session) {
	if r == nil {
		return
	}
	ds, ok := ss.(mux.DatagramSession)
	if !ok || ds.Datagrams() == nil || ss.Stats() == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := ds.Datagrams()
	u := &udpSession{ss: ss, d: d, metrics: ss.Stats(), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), flows: make(map[uint16]*udpFlow), next: 2 - d.LocalParity()}
	cfg, _ := r.s.getConfig()
	d.SetQueueLimit(cfg.Mux.LossyMux.Queue)
	r.mu.Lock()
	if r.closed || r.sessions[ss] != nil {
		r.mu.Unlock()
		cancel()
		return
	}
	r.sessions[ss] = u
	r.mu.Unlock()
	if err := r.s.g.Go(func() {
		defer close(u.done)
		defer r.detach(ss)
		for {
			v, err := d.Receive(ctx)
			if err != nil {
				return
			}
			r.receive(u, v)
		}
	}); err != nil {
		close(u.done)
		r.detach(ss)
	}
}

// forgetLocked removes a flow exactly once and returns its final socket cleanup.
// The caller runs cleanup after unlocking.
func (r *udpRelay) forgetLocked(f *udpFlow) (bool, func()) {
	if f.owner.flows[f.id] != f {
		return false, nil
	}
	delete(f.owner.flows, f.id)
	f.owner.metrics.NumUDPFlows.Add(-1)
	if f.listener == nil {
		return true, func() { _ = f.conn.Close() }
	}
	l := f.listener
	if l.routes[f.client] == f {
		delete(l.routes, f.client)
	}
	l.refs--
	if l.refs == 0 {
		return true, func() { r.closeListener(l) }
	}
	return true, nil
}

func (r *udpRelay) dropFlow(f *udpFlow, notify bool) {
	r.mu.Lock()
	removed, conn := r.forgetLocked(f)
	r.mu.Unlock()
	if conn != nil {
		conn()
	}
	if removed && notify {
		_ = f.owner.d.TryClose(f.id)
	}
}

func (r *udpRelay) detach(ss mux.Session) <-chan struct{} {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	u := r.sessions[ss]
	if u == nil {
		r.mu.Unlock()
		return nil
	}
	delete(r.sessions, ss)
	u.cancel()
	var conns []func()
	for _, f := range u.flows {
		_, conn := r.forgetLocked(f)
		if conn != nil {
			conns = append(conns, conn)
		}
	}
	r.mu.Unlock()
	for _, conn := range conns {
		conn()
	}
	return u.done
}

func (r *udpRelay) detachAndWait(ss mux.Session) {
	if done := r.detach(ss); done != nil {
		<-done
	}
	if ds, ok := ss.(mux.DatagramSession); ok {
		ds.Datagrams().Wait()
	}
}

func (r *udpRelay) allocIDLocked(u *udpSession) uint16 {
	for i := 0; i < 32768; i++ {
		id := u.next
		if id >= 65534 {
			u.next = 2 - u.d.LocalParity()
		} else {
			u.next += 2
		}
		if u.flows[id] == nil {
			return id
		}
	}
	return 0
}

func (r *udpRelay) localDatagram(l *udpListener, client netip.AddrPort, data []byte) {
	// Unmap IPv4-mapped addresses so client keys are stable across Go APIs.
	client = netip.AddrPortFrom(client.Addr().Unmap(), client.Port())
	r.mu.Lock()
	if !l.active || r.closed {
		r.mu.Unlock()
		r.unrouted.Add(1)
		return
	}
	f := l.routes[client]
	id := l.id
	if f != nil && f.owner.ss.IsClosed() {
		f = nil // detach owns removal; this packet may select the replacement
	}
	if f != nil {
		f.lastSeen = time.Now()
	}
	r.mu.Unlock()
	if f != nil {
		r.send(f.owner, f.id, data)
		return
	}

	t := r.s.findSession(id)
	if t == nil {
		r.unrouted.Add(1)
		return
	}
	ss := t.getSession()
	if ss == nil {
		// Unlike a TCP open, demand redial never holds the triggering datagram.
		t.demandRedial()
		r.unrouted.Add(1)
		return
	}
	r.mu.Lock()
	u := r.sessions[ss]
	if u == nil || !u.d.Ready() || !l.active || r.closed {
		r.mu.Unlock()
		if m := ss.Stats(); m != nil {
			m.DatagramsDropped.Add(1)
		} else {
			r.unrouted.Add(1)
		}
		return
	}
	// Only this listener's reader creates its active flows.
	flowID := r.allocIDLocked(u)
	if flowID == 0 {
		r.mu.Unlock()
		u.metrics.DatagramsDropped.Add(1)
		return
	}
	f = &udpFlow{owner: u, id: flowID, lastSeen: time.Now(), listener: l, client: client}
	u.flows[flowID] = f
	l.routes[client] = f
	l.refs++
	u.metrics.NumUDPFlows.Add(1)
	r.mu.Unlock()
	r.send(u, flowID, data)
}

func (r *udpRelay) readListener(l *udpListener) {
	// Larger than every non-jumbo UDP payload, so truncated payloads are never
	// mistaken for complete packets, including on IPv6.
	buf := make([]byte, 65536)
	for {
		n, client, err := l.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			r.unrouted.Add(1)
			continue
		}
		if n > 65535 {
			r.unrouted.Add(1)
			continue
		}
		r.localDatagram(l, client, buf[:n])
	}
}

func (r *udpRelay) openPassive(u *udpSession, id uint16) *udpFlow {
	cfg, _ := r.s.getConfig()
	if cfg.Connect == "" {
		return nil
	}
	dialer := net.Dialer{Timeout: cfg.ConnectTimeout()}
	conn, err := dialer.DialContext(u.ctx, "udp", cfg.Connect)
	if err != nil {
		return nil
	}
	c := conn.(*net.UDPConn)
	f := &udpFlow{owner: u, id: id, lastSeen: time.Now(), conn: c}
	r.mu.Lock()
	if r.closed || r.sessions[u.ss] != u || u.ss.IsClosed() {
		r.mu.Unlock()
		_ = c.Close()
		return nil
	}
	u.flows[id] = f
	u.metrics.NumUDPFlows.Add(1)
	r.mu.Unlock()
	if err := r.s.g.Go(func() { r.readPassive(f) }); err != nil {
		r.dropFlow(f, false)
		return nil
	}
	return f
}

func (r *udpRelay) receive(u *udpSession, v mux.Datagram) {
	r.mu.Lock()
	if r.sessions[u.ss] != u {
		r.mu.Unlock()
		return
	}
	f := u.flows[v.FlowID]
	r.mu.Unlock()
	if v.Close {
		if f != nil {
			r.dropFlow(f, false)
		}
		return
	}
	if f == nil && v.FlowID&1 != u.d.LocalParity() {
		f = r.openPassive(u, v.FlowID)
	}
	if f == nil {
		u.metrics.DatagramsDropped.Add(1)
		_ = u.d.TryClose(v.FlowID)
		return
	}
	r.mu.Lock()
	alive := u.flows[f.id] == f
	if alive {
		f.lastSeen = time.Now()
	}
	r.mu.Unlock()
	if !alive {
		u.metrics.DatagramsDropped.Add(1)
		return
	}
	cfg, _ := r.s.getConfig()
	deadline := time.Now().Add(cfg.SendTimeout())
	var err error
	if f.listener != nil {
		l := f.listener
		l.writeMu.Lock()
		_ = l.conn.SetWriteDeadline(deadline)
		_, err = l.conn.WriteToUDPAddrPort(v.Data, f.client)
		l.writeMu.Unlock()
	} else {
		_ = f.conn.SetWriteDeadline(deadline)
		_, err = f.conn.Write(v.Data)
	}
	if err != nil {
		u.metrics.DatagramsDropped.Add(1)
	}
}

func (r *udpRelay) readPassive(f *udpFlow) {
	defer r.dropFlow(f, true)
	buf := make([]byte, 65536)
	for {
		n, err := f.conn.Read(buf)
		if err != nil {
			return
		}
		if n > 65535 {
			f.owner.metrics.DatagramsDropped.Add(1)
			continue
		}
		r.mu.Lock()
		alive := f.owner.flows[f.id] == f
		if alive {
			f.lastSeen = time.Now()
		}
		r.mu.Unlock()
		if !alive {
			return
		}
		r.send(f.owner, f.id, buf[:n])
	}
}

// syncListeners follows settled TCP addresses, including ephemeral ports. A
// retired socket is retained for replies and can be reused on a later reload.
func (r *udpRelay) syncListeners(cfg *config.File) error {
	desired := make(map[string]string)
	if cfg.Mux.LossyMux.Enabled {
		r.s.mu.RLock()
		if l := r.s.localListener; l != nil {
			desired[""] = l.Addr().String()
		}
		for id, il := range r.s.identities {
			desired[id] = il.l.Addr().String()
		}
		r.s.mu.RUnlock()
	}
	r.mu.Lock()
	var closeConns []func()
	for id, l := range r.listeners {
		if desired[id] == l.addr {
			continue
		}
		delete(r.listeners, id)
		l.active = false
		l.refs--
		if l.refs == 0 {
			closeConns = append(closeConns, func() { r.closeListener(l) })
		}
	}
	for _, u := range r.sessions {
		u.d.SetQueueLimit(cfg.Mux.LossyMux.Queue)
	}
	r.mu.Unlock()
	for _, conn := range closeConns {
		conn()
	}
	var errs []error

	for id, addr := range desired {
		if err := r.syncListener(id, addr); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Keep zero-reference sockets registered until Close finishes. Otherwise a
// reload racing expiry could bind too early or resurrect a socket being closed.
func (r *udpRelay) closeListener(l *udpListener) {
	_ = l.conn.Close()
	r.mu.Lock()
	delete(r.sockets, l.addr)
	close(l.closed)
	r.mu.Unlock()
}

func (r *udpRelay) syncListener(id, addr string) error {
	for {
		r.mu.Lock()
		if r.listeners[id] != nil || r.closed {
			r.mu.Unlock()
			return nil
		}
		if l := r.sockets[addr]; l != nil {
			if l.refs == 0 {
				r.mu.Unlock()
				select {
				case <-l.closed:
					continue
				case <-r.s.g.CloseC():
					return nil
				}
			}
			if l.active {
				r.mu.Unlock()
				return fmt.Errorf("UDP listen %s: address already routed", addr)
			}
			if l.id != id {
				l.routes = make(map[netip.AddrPort]*udpFlow)
			}
			l.active, l.id = true, id
			l.refs++
			r.listeners[id] = l
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
		uaddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return err
		}
		conn, err := net.ListenUDP("udp", uaddr)
		if err != nil {
			return fmt.Errorf("UDP listen %s: %w", addr, err)
		}
		l := &udpListener{conn: conn, id: id, addr: addr, active: true, refs: 1, closed: make(chan struct{}), routes: make(map[netip.AddrPort]*udpFlow)}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		r.listeners[id], r.sockets[addr] = l, l
		r.mu.Unlock()
		if err := r.s.g.Go(func() { r.readListener(l) }); err != nil {
			r.mu.Lock()
			delete(r.listeners, id)
			l.active = false
			l.refs--
			r.mu.Unlock()
			r.closeListener(l)
			return err
		}
		return nil
	}
}

func (r *udpRelay) sweep(now time.Time) {
	cfg, _ := r.s.getConfig()
	idle := time.Duration(cfg.Mux.LossyMux.IdleTimeout) * time.Second
	r.mu.Lock()
	var expired []*udpFlow
	var conns []func()
	for _, u := range r.sessions {
		for _, f := range u.flows {
			if now.Sub(f.lastSeen) < idle {
				continue
			}
			_, conn := r.forgetLocked(f)
			expired = append(expired, f)
			if conn != nil {
				conns = append(conns, conn)
			}
		}
	}
	r.mu.Unlock()
	for _, conn := range conns {
		conn()
	}
	for _, f := range expired {
		_ = f.owner.d.TryClose(f.id)
	}
}

func (r *udpRelay) run() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			r.sweep(now)
			r.mu.Lock()
			active := len(r.sessions) > 0
			r.mu.Unlock()
			if !active {
				continue
			}
			for _, t := range r.s.getAllTunnels() {
				if ds, ok := t.getSession().(mux.DatagramSession); ok && ds.Datagrams() != nil {
					t.checkIdle()
				}
			}
		case <-r.s.g.CloseC():
			return
		}
	}
}

func (r *udpRelay) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	conns := make([]*net.UDPConn, 0, len(r.sockets))
	for _, l := range r.sockets {
		l.active = false
		conns = append(conns, l.conn)
	}
	sessions := make([]mux.Session, 0, len(r.sessions))
	for ss := range r.sessions {
		sessions = append(sessions, ss)
	}
	r.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	for _, ss := range sessions {
		r.detach(ss)
	}
}

func (r *udpRelay) send(u *udpSession, flow uint16, data []byte) {
	if err := u.d.TrySend(flow, data); errors.Is(err, mux.ErrSessionClosed) {
		// A packet racing finalization belongs to the server's offline drops; the
		// session's counters may already have been folded into its archive.
		r.unrouted.Add(1)
	}
}
