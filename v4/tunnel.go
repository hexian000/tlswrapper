// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/mux"
)

// tunnel owns at most one active mux session.
// Config-driven tunnels are tracked in Server.services and may also own a
// listener plus a redial loop. Inbound tunnels are created for accepted
// sessions and disappear with that session.
type tunnel struct {
	id       string // config key or remote identity; used for lookup and logging
	dialAddr string // outbound dial target; empty for inbound accepted sessions
	s        *Server
	l        net.Listener // local TCP listener (only on config-driven tunnels with Listen)

	mu            sync.RWMutex
	tag           string
	ss            mux.Session
	idleSince     time.Time // when ss became stream-less (zero = not idle)
	stale         bool      // marked after config reload; evicted when idle
	closeSig      chan struct{}
	stopOnce      sync.Once
	redialSig     chan struct{}
	redialCount   int
	dialMu        sync.Mutex
	lastChanged   time.Time
	streamLatency latencyRing
	muxStats      *snet.FlowStats // raw socket bytes for the current mux connection
}

func newTunnel(id, dialAddr string, s *Server) *tunnel {
	t := &tunnel{
		id:        id,
		dialAddr:  dialAddr,
		s:         s,
		closeSig:  make(chan struct{}),
		redialSig: make(chan struct{}, 1),
		muxStats:  &snet.FlowStats{},
	}
	t.tag = t.buildTunnelTag(nil, nil)
	return t
}

func (t *tunnel) getConfig() (*config.File, *tls.Config) {
	return t.s.getConfig()
}

func (t *tunnel) defaultDirectionOutbound() bool {
	return t.dialAddr != ""
}

func resolveMeLabel(identity string, localAddr net.Addr, conn net.Conn) string {
	if identity != "" {
		return identity
	}
	if localAddr == nil && conn != nil {
		localAddr = conn.LocalAddr()
	}
	if localAddr != nil {
		if s := localAddr.String(); s != "" {
			return s
		}
	}
	return "?"
}

func resolvePeerLabel(peerIdentity, peerID string, peerAddr net.Addr, conn net.Conn) string {
	if peerIdentity != "" {
		return peerIdentity
	}
	if peerID != "" {
		return peerID
	}
	if peerAddr == nil && conn != nil {
		peerAddr = conn.RemoteAddr()
	}
	if peerAddr != nil {
		if s := peerAddr.String(); s != "" {
			return s
		}
	}
	return "?"
}

func formatTunnelTag(outbound bool, identity, peerIdentity, peerID string, localAddr, peerAddr net.Addr, conn net.Conn) string {
	arrow := "<="
	if outbound {
		arrow = "=>"
	}
	me := resolveMeLabel(identity, localAddr, conn)
	peer := resolvePeerLabel(peerIdentity, peerID, peerAddr, conn)
	return fmt.Sprintf("%s %s %s", me, arrow, peer)
}

func formatStreamTag(outbound bool, identity, peerIdentity, peerID string, localAddr, peerAddr net.Addr, conn net.Conn) string {
	arrow := "<-"
	if outbound {
		arrow = "->"
	}
	me := resolveMeLabel(identity, localAddr, conn)
	peer := resolvePeerLabel(peerIdentity, peerID, peerAddr, conn)
	return fmt.Sprintf("%s %s %s", me, arrow, peer)
}

func (t *tunnel) buildTunnelTag(ss mux.Session, conn net.Conn) string {
	cfg, _ := t.getConfig()
	peerID := t.id
	var peerIdentity string
	var localAddr, remoteAddr net.Addr
	if ss != nil {
		peerIdentity = ss.PeerID()
		localAddr = ss.LocalAddr()
		remoteAddr = ss.RemoteAddr()
	}
	return formatTunnelTag(t.defaultDirectionOutbound(), cfg.Identity.Claim, peerIdentity, peerID, localAddr, remoteAddr, conn)
}

func (t *tunnel) updateTagLocked(ss mux.Session, conn net.Conn) {
	t.tag = t.buildTunnelTag(ss, conn)
}

func (t *tunnel) tagValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tag
}

// Start applies the current config to a config-driven tunnel.
func (t *tunnel) Start(cfg *config.File) error {
	if listenAddr := cfg.ServiceEntry(t.id).Listen; listenAddr != "" {
		l, err := t.s.Listen(listenAddr)
		if err != nil {
			return err
		}
		tag := t.tagValue()
		slog.Noticef("%s: listen %v", tag, l.Addr())
		h := &LocalHandler{l: l, s: t.s, id: t.id}
		if err := t.s.g.Go(func() {
			t.s.Serve(l, h)
		}); err != nil {
			ioClose(l)
			return err
		}
		t.l = l
	}
	if t.dialAddr != "" {
		tag := t.tagValue()
		slog.Debugf("%s: start outbound", tag)
		return t.s.g.Go(t.run)
	}
	return nil
}

// Stop closes the listener and current session once.
func (t *tunnel) Stop() error {
	t.stopOnce.Do(func() {
		close(t.closeSig)
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.l != nil {
			ioClose(t.l)
			t.l = nil
		}
		if t.ss != nil {
			ioClose(t.ss)
			t.ss = nil
		}
		tag := t.tag
		slog.Debugf("%s: stop", tag)
	})
	return nil
}

func (t *tunnel) checkIdle() {
	cfg, _ := t.getConfig()
	idleTimeout := time.Duration(cfg.Mux.IdleTimeout) * time.Second
	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ss == nil {
		return
	}
	if t.ss.IsClosed() {
		t.ss = nil
		t.idleSince = time.Time{}
		return
	}
	// evict stale sessions immediately when idle (no active streams)
	m := t.ss.Stats()
	var numStreams int64
	if m != nil {
		numStreams = m.NumStreams.Load()
	}
	if t.stale && numStreams == 0 {
		tag := t.tag
		slog.Infof("%s: stale session evicted after reload", tag)
		_ = t.ss.Close()
		t.ss = nil
		t.idleSince = time.Time{}
		return
	}
	// update idle tracking
	if numStreams == 0 {
		if t.idleSince.IsZero() {
			t.idleSince = now
		}
	} else {
		t.idleSince = time.Time{}
	}
	// evict if idle too long
	if idleTimeout > 0 && !t.idleSince.IsZero() && now.Sub(t.idleSince) >= idleTimeout {
		tag := t.tag
		slog.Infof("%s: idle session evicted after %v", tag, now.Sub(t.idleSince))
		_ = t.ss.Close()
		t.ss = nil
		t.idleSince = time.Time{}
	}
}

func (t *tunnel) redial() {
	ctx := t.s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer t.s.ctx.cancel(ctx)
	_, err := t.dial(ctx)
	if err != nil && !errors.Is(err, ErrNoDialAddress) && !errors.Is(err, ErrDialInProgress) {
		redialCount := t.redialCount + 1
		if redialCount > t.redialCount {
			t.redialCount = redialCount
		}
		tag := t.tagValue()
		slog.Infof("%s: redial #%d to %s: %s", tag, t.redialCount, t.dialAddr, formats.Error(err))
		return
	}
	t.redialCount = 0
}

func (t *tunnel) maintenance() {
	t.checkIdle()
	if t.getSession() == nil {
		cfg, _ := t.getConfig()
		if !cfg.NoRedial && t.dialAddr != "" {
			t.redial()
		}
		return
	}
}

func (t *tunnel) schedule() <-chan time.Time {
	cfg, _ := t.getConfig()
	if cfg.NoRedial || t.dialAddr == "" || t.redialCount < 1 {
		pause := 10 * time.Minute
		pause += time.Duration(rand.Int63n(int64(10 * time.Minute)))
		return time.After(pause)
	}
	n := t.redialCount - 1
	var waitTimeConst = [...]time.Duration{
		200 * time.Millisecond,
		2 * time.Second,
		2 * time.Second,
		5 * time.Second,
		5 * time.Second,
		15 * time.Second,
		15 * time.Second,
		15 * time.Second,
		1 * time.Minute,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
	}
	waitTime := waitTimeConst[len(waitTimeConst)-1]
	if n < len(waitTimeConst) {
		waitTime = waitTimeConst[n]
	}
	tag := t.tagValue()
	slog.Debugf("%s: redial scheduled after %v", tag, waitTime)
	return time.After(waitTime)
}

func (t *tunnel) run() {
	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.l != nil {
			slog.Infof("listener close: %v", t.l.Addr())
			ioClose(t.l)
			t.l = nil
		}
		if t.ss != nil {
			ioClose(t.ss)
			t.ss = nil
		}
	}()
	for {
		t.maintenance()
		select {
		case <-t.closeSig:
			return
		case <-t.redialSig:
		case <-t.schedule():
		case <-t.s.g.CloseC():
			return
		}
	}
}

func (t *tunnel) addSession(ss mux.Session, setupDur time.Duration) {
	now := time.Now()
	t.mu.Lock()
	hadConn := t.ss != nil && !t.ss.IsClosed()
	t.ss = ss
	t.updateTagLocked(ss, nil)
	tag := t.tag
	t.stale = false
	if !hadConn {
		t.s.stats.numSessions.Add(1)
		t.s.stats.numSessionsCreated.Add(1)
	}
	t.lastChanged = now
	t.mu.Unlock()

	msg := fmt.Sprintf("%s: session established (setup: %s)", tag, formats.Duration(setupDur))
	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)
}

func (t *tunnel) delSession(ss mux.Session) {
	now := time.Now()
	t.mu.Lock()
	if t.ss != ss {
		t.mu.Unlock()
		return
	}
	tag := t.tag
	msg := fmt.Sprintf("%s: session closed", tag)
	t.s.flushSessionMetrics(t, ss)
	t.ss = nil
	t.idleSince = time.Time{}
	t.s.stats.numSessions.Add(^uint32(0))
	t.s.stats.numSessionsFinalized.Add(1)
	t.lastChanged = now
	if t.dialAddr != "" {
		select {
		case t.redialSig <- struct{}{}:
		default:
		}
	}
	t.mu.Unlock()

	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)
}

func (t *tunnel) getSession() mux.Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.ss == nil || t.ss.IsClosed() {
		return nil
	}
	return t.ss
}

func (t *tunnel) OpenStream(ctx context.Context) (net.Conn, error) {
	ss := t.getSession()
	if ss == nil {
		return nil, ErrNoSession
	}
	start := time.Now()
	conn, err := ss.Open(ctx)
	if err != nil {
		return nil, err
	}
	t.streamLatency.Record(time.Since(start))
	return conn, nil
}

// dial establishes a new outbound mux session.
func (t *tunnel) dial(ctx context.Context) (mux.Session, error) {
	cfg, tlscfg := t.getConfig()
	if t.dialAddr == "" {
		return nil, ErrNoDialAddress
	}
	if !t.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer t.dialMu.Unlock()
	start := time.Now()
	rawConn, err := t.s.dialer.DialContext(ctx, network, t.dialAddr)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.updateTagLocked(nil, rawConn)
	tag := t.tag
	t.mu.Unlock()
	if tlscfg == nil {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	setTCPConnParams(cfg.Mux.TCP, rawConn)
	t.muxStats = &snet.FlowStats{}
	conn := snet.FlowMeter(rawConn, t.muxStats)
	h2cfg := &mux.Config{
		TLSConfig:     tlscfg,
		LocalID:       cfg.Identity.Claim,
		KeepAlive:     time.Duration(cfg.Mux.KeepAlive) * time.Second,
		PingTimeout:   time.Duration(cfg.Mux.PingTimeout) * time.Second,
		WriteTimeout:  time.Duration(cfg.Mux.SendTimeout) * time.Second,
		SessionWindow: int32(cfg.Mux.SessionWindow),
		StreamWindow:  int32(cfg.Mux.StreamWindow),
	}
	ss, err := mux.Client(ctx, conn, h2cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	t.addSession(ss, time.Since(start))
	// When the session closes, trigger a redial.
	if err := t.s.g.Go(func() {
		defer t.delSession(ss)
		<-ss.CloseChan()
	}); err != nil {
		t.delSession(ss)
		return nil, err
	}
	// Close ss when the group shuts down, to unblock the accept loop.
	// This is needed in particular after a redial, where t.ss has moved on
	// and t.run's defer will no longer close this specific ss.
	if err := t.s.g.Go(func() {
		select {
		case <-t.s.g.CloseC():
			_ = ss.Close()
		case <-ss.CloseChan():
		}
	}); err != nil {
		_ = ss.Close()
		return nil, err
	}
	// Accept server-initiated streams so that dialStreamForServer conns do not
	// pile up in acceptCh and numStreams never decrements (the stream leak).
	if err := t.s.g.Go(func() {
		t.s.acceptInboundStreams(t, ss)
	}); err != nil {
		_ = ss.Close()
		return nil, err
	}

	return ss, nil
}

// SessionStats snapshots the most recent session state for one tunnel key.
type SessionStats struct {
	Name        string
	LastChanged time.Time
	Active      bool
	// gRPC transport statistics; zero when unavailable.
	StreamsOpened      uint64
	StreamsAccepted    uint64
	StreamsSucceeded   uint64
	StreamsFailed      uint64
	NumStreams         uint32
	BytesSent          uint64
	BytesReceived      uint64
	WireLengthSent     uint64
	WireLengthReceived uint64
	// StreamLatency holds pre-computed percentiles of this tunnel's
	// stream-open latency ring.
	StreamLatency StreamLatencyStats
}

// Stats snapshots the current session state.
func (t *tunnel) Stats() SessionStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	active := t.ss != nil && !t.ss.IsClosed()
	name := t.id
	var streamsOpened, streamsAccepted, streamsSucceeded, streamsFailed, bytesSent, bytesReceived, wireLengthSent, wireLengthReceived uint64
	var numStreams uint32
	if active {
		if peerID := t.ss.PeerID(); peerID != "" {
			name = peerID
		}
		if m := t.ss.Stats(); m != nil {
			streamsOpened = uint64(m.StreamsOpened.Load())
			streamsAccepted = uint64(m.StreamsAccepted.Load())
			streamsSucceeded = uint64(m.StreamsSucceeded.Load())
			streamsFailed = uint64(m.StreamsFailed.Load())
			numStreams = uint32(m.NumStreams.Load())
			bytesSent = uint64(m.BytesSent.Load())
			bytesReceived = uint64(m.BytesReceived.Load())
			wireLengthSent = uint64(m.WireLengthSent.Load())
			wireLengthReceived = uint64(m.WireLengthReceived.Load())
		}
	}
	p50, p90, p99, pmax, latOk := t.streamLatency.Percentiles()
	return SessionStats{
		Name:               name,
		LastChanged:        t.lastChanged,
		Active:             active,
		StreamsOpened:      streamsOpened,
		StreamsAccepted:    streamsAccepted,
		StreamsSucceeded:   streamsSucceeded,
		StreamsFailed:      streamsFailed,
		NumStreams:         numStreams,
		BytesSent:          bytesSent,
		BytesReceived:      bytesReceived,
		WireLengthSent:     wireLengthSent,
		WireLengthReceived: wireLengthReceived,
		StreamLatency:      StreamLatencyStats{P50: p50, P90: p90, P99: p99, Max: pmax, Available: latOk},
	}
}
