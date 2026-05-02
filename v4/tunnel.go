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

// tunnel manages exactly one mux connection slot for a named peer.
// Config-driven tunnels (tracked in Server.services) are created from config and
// can own listeners plus a redial loop.
// Inbound tunnels (dialAddr == "") are created by serveSession for accepted mux
// connections and are removed when that connection closes.
type tunnel struct {
	id       string // peer service ID; used for logging and lookup
	dialAddr string // MuxConnect address; empty for inbound ephemeral tunnels
	s        *Server
	l        net.Listener // local TCP listener (only on config-driven tunnels with Listen)

	mu          sync.RWMutex
	ss          *mux.Session
	idleSince   time.Time // when ss became stream-less (zero = not idle)
	closeSig    chan struct{}
	stopOnce    sync.Once
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func newTunnel(id, dialAddr string, s *Server) *tunnel {
	return &tunnel{
		id:        id,
		dialAddr:  dialAddr,
		s:         s,
		closeSig:  make(chan struct{}),
		redialSig: make(chan struct{}, 1),
	}
}

func (t *tunnel) getConfig() (*config.File, *tls.Config) {
	return t.s.getConfig()
}

// Start starts a config-driven tunnel from the provided config snapshot.
// When dialAddr != "", it also starts the redial loop for that tunnel.
func (t *tunnel) Start(cfg *config.File) error {
	if listenAddr := cfg.ServiceEntry(t.id).Listen; listenAddr != "" {
		l, err := t.s.Listen(listenAddr)
		if err != nil {
			return err
		}
		slog.Noticef("session %q: listen %v", t.id, l.Addr())
		h := &MuxHandler{l: l, s: t.s, id: t.id}
		if err := t.s.g.Go(func() {
			t.s.Serve(l, h)
		}); err != nil {
			ioClose(l)
			return err
		}
		t.l = l
	}
	if t.dialAddr != "" {
		slog.Debugf("session %q: start outbound", t.id)
		return t.s.g.Go(t.run)
	}
	return nil
}

// Stop stops the tunnel lifecycle and closes its listener during shutdown.
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
		slog.Debugf("session %q: stop", t.id)
	})
	return nil
}

func (t *tunnel) checkIdle() {
	cfg, _ := t.getConfig()
	idleTimeout := time.Duration(cfg.IdleTimeout) * time.Second
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
	// update idle tracking
	if t.ss.NumStreams() == 0 {
		if t.idleSince.IsZero() {
			t.idleSince = now
		}
	} else {
		t.idleSince = time.Time{}
	}
	// evict if idle too long
	if idleTimeout > 0 && !t.idleSince.IsZero() && now.Sub(t.idleSince) >= idleTimeout {
		slog.Infof("session %q: idle session evicted after %v", t.id, now.Sub(t.idleSince))
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
		cfg, _ := t.getConfig()
		slog.Infof("session %q: redial #%d to %s: %s", t.id, t.redialCount, cfg.ServiceEntry(t.id).MuxConnect, formats.Error(err))
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
	slog.Debugf("session %q: redial scheduled after %v", t.id, waitTime)
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

func (t *tunnel) addSession(ss *mux.Session) {
	now := time.Now()
	msg := fmt.Sprintf("%s: session established", ss.Tag())
	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)

	t.mu.Lock()
	defer t.mu.Unlock()
	hadConn := t.ss != nil && !t.ss.IsClosed()
	t.ss = ss
	if !hadConn {
		t.s.numSessions.Add(1)
	}
	t.lastChanged = now
}

func (t *tunnel) delSession(ss *mux.Session) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ss != ss {
		return
	}
	msg := fmt.Sprintf("%s: session closed", ss.Tag())
	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)
	t.ss = nil
	t.idleSince = time.Time{}
	t.s.numSessions.Add(^uint32(0))
	t.lastChanged = now
	if t.dialAddr != "" {
		select {
		case t.redialSig <- struct{}{}:
		default:
		}
	}
}

func (t *tunnel) getSession() *mux.Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.ss == nil || t.ss.IsClosed() {
		return nil
	}
	return t.ss
}

// OpenStream opens a new stream over the session's active mux connection.
func (t *tunnel) OpenStream(ctx context.Context) (net.Conn, error) {
	ss := t.getSession()
	if ss == nil {
		return nil, ErrNoSession
	}
	return ss.Open(ctx)
}

// dial dials the configured peer and establishes the mux session for this tunnel.
func (t *tunnel) dial(ctx context.Context) (*mux.Session, error) {
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
	tag := fmt.Sprintf("%q => %v", t.id, rawConn.RemoteAddr())
	if tlscfg == nil {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	setTCPConnParams(cfg.Mux.TCP, rawConn)
	conn := snet.FlowMeter(rawConn, t.s.flowStats)
	h2cfg := &mux.Config{
		TLSConfig:     tlscfg,
		LocalID:       cfg.Service.ID,
		KeepAlive:     time.Duration(cfg.KeepAlive) * time.Second,
		PingTimeout:   time.Duration(cfg.PingTimeout) * time.Second,
		WriteTimeout:  time.Duration(cfg.SendTimeout) * time.Second,
		SessionWindow: int32(cfg.Mux.SessionWindow),
		StreamWindow:  int32(cfg.Mux.StreamWindow),
	}
	ss, err := mux.Client(ctx, conn, h2cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	t.addSession(ss)
	// When the session closes, trigger a redial.
	if err := t.s.g.Go(func() {
		defer t.delSession(ss)
		<-ss.CloseChan()
	}); err != nil {
		t.delSession(ss)
		return nil, err
	}

	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	return ss, nil
}

// SessionStats holds statistics of a session.
type SessionStats struct {
	Name        string
	LastChanged time.Time
	NumStreams  int
	Active      bool
}

// Stats returns the current statistics of the session.
func (t *tunnel) Stats() SessionStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	active := t.ss != nil && !t.ss.IsClosed()
	numStreams := 0
	if active {
		numStreams = t.ss.NumStreams()
	}
	return SessionStats{
		Name:        t.id,
		LastChanged: t.lastChanged,
		NumStreams:  numStreams,
		Active:      active,
	}
}
