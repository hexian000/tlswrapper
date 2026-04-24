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
	"github.com/hexian000/tlswrapper/v4/h2mux"
)

// session manages exactly one outbound or inbound mux connection for a named peer.
// Outbound sessions (dialAddr != "") own a redial loop in run().
// Inbound sessions (dialAddr == "") are created by serveH2Conn and held for lookup.
type session struct {
	id       string // peer service ID; used for logging and lookup
	dialAddr string // MuxConnect address; empty for inbound sessions
	s        *Server
	l        net.Listener // local TCP listener (only on config-driven sessions with Listen)

	mu          sync.RWMutex
	h2sess      *h2mux.Session
	idleSince   time.Time // when h2sess became stream-less (zero = not idle)
	closeSig    chan struct{}
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func newSession(peerServiceId, dialAddr string, s *Server) *session {
	return &session{
		id:        peerServiceId,
		dialAddr:  dialAddr,
		s:         s,
		closeSig:  make(chan struct{}),
		redialSig: make(chan struct{}, 1),
	}
}

func (ss *session) getConfig() (*config.File, *tls.Config) {
	return ss.s.getConfig()
}

// Start starts the session, including listening if configured.
// For outbound sessions (dialAddr != ""), a redial goroutine is also started.
func (ss *session) Start() error {
	cfg, _ := ss.getConfig()
	if listenAddr := cfg.ServiceEntry(ss.id).Listen; listenAddr != "" {
		l, err := ss.s.Listen(listenAddr)
		if err != nil {
			return err
		}
		slog.Noticef("session %q: listen %v", ss.id, l.Addr())
		h := &MuxHandler{l: l, s: ss.s, id: ss.id}
		if err := ss.s.g.Go(func() {
			ss.s.Serve(l, h)
		}); err != nil {
			ioClose(l)
			return err
		}
		ss.l = l
	}
	if ss.dialAddr != "" {
		slog.Debugf("session %q: start outbound", ss.id)
		return ss.s.g.Go(ss.run)
	}
	return nil
}

// Stop stops the session's redial loop (and closes any local listener).
func (ss *session) Stop() error {
	close(ss.closeSig)
	slog.Debugf("session %q: stop", ss.id)
	return nil
}

func (ss *session) cleanSession() {
	cfg, _ := ss.getConfig()
	idleTimeout := time.Duration(cfg.IdleTimeout) * time.Second
	now := time.Now()

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.h2sess == nil {
		return
	}
	if ss.h2sess.IsClosed() {
		ss.h2sess = nil
		ss.idleSince = time.Time{}
		return
	}
	// update idle tracking
	if ss.h2sess.NumStreams() == 0 {
		if ss.idleSince.IsZero() {
			ss.idleSince = now
		}
	} else {
		ss.idleSince = time.Time{}
	}
	// evict if idle too long
	if idleTimeout > 0 && !ss.idleSince.IsZero() && now.Sub(ss.idleSince) >= idleTimeout {
		slog.Debugf("session %q: idle session evicted after %v", ss.id, now.Sub(ss.idleSince))
		_ = ss.h2sess.Close()
		ss.h2sess = nil
		ss.idleSince = time.Time{}
	}
}

func (ss *session) redial() {
	ctx := ss.s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer ss.s.ctx.cancel(ctx)
	_, err := ss.h2Dial(ctx)
	if err != nil && !errors.Is(err, ErrNoDialAddress) && !errors.Is(err, ErrDialInProgress) {
		redialCount := ss.redialCount + 1
		if redialCount > ss.redialCount {
			ss.redialCount = redialCount
		}
		cfg, _ := ss.getConfig()
		slog.Warningf("session %q: redial #%d to %s: %s", ss.id, ss.redialCount, cfg.ServiceEntry(ss.id).MuxConnect, formats.Error(err))
		return
	}
	ss.redialCount = 0
}

func (ss *session) maintenance() {
	ss.cleanSession()
	if ss.getH2sess() == nil {
		cfg, _ := ss.getConfig()
		if !cfg.NoRedial && ss.dialAddr != "" {
			ss.redial()
		}
		return
	}
}

func (ss *session) schedule() <-chan time.Time {
	cfg, _ := ss.getConfig()
	if cfg.NoRedial || ss.dialAddr == "" || ss.redialCount < 1 {
		pause := 10 * time.Minute
		pause += time.Duration(rand.Int63n(int64(10 * time.Minute)))
		return time.After(pause)
	}
	n := ss.redialCount - 1
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
	slog.Debugf("session %q: redial scheduled after %v", ss.id, waitTime)
	return time.After(waitTime)
}

func (ss *session) run() {
	defer func() {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		if ss.l != nil {
			slog.Infof("listener close: %v", ss.l.Addr())
			ioClose(ss.l)
			ss.l = nil
		}
		if ss.h2sess != nil {
			_ = ss.h2sess.Close()
			ss.h2sess = nil
		}
	}()
	for {
		ss.maintenance()
		select {
		case <-ss.closeSig:
			return
		case <-ss.redialSig:
		case <-ss.schedule():
		case <-ss.s.g.CloseC():
			return
		}
	}
}

func (ss *session) addH2sess(h2sess *h2mux.Session) {
	now := time.Now()
	msg := fmt.Sprintf("%s: session established", h2sess.Tag())
	slog.Notice(msg)
	ss.s.recentEvents.Add(now, msg)

	ss.mu.Lock()
	defer ss.mu.Unlock()
	hadConn := ss.h2sess != nil && !ss.h2sess.IsClosed()
	ss.h2sess = h2sess
	if !hadConn {
		ss.s.numSessions.Add(1)
	}
	ss.lastChanged = now
}

func (ss *session) delH2sess(h2sess *h2mux.Session) {
	now := time.Now()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.h2sess != h2sess {
		return
	}
	msg := fmt.Sprintf("%s: session closed", h2sess.Tag())
	slog.Notice(msg)
	ss.s.recentEvents.Add(now, msg)
	ss.h2sess = nil
	ss.idleSince = time.Time{}
	ss.s.numSessions.Add(^uint32(0))
	ss.lastChanged = now
	if ss.dialAddr != "" {
		select {
		case ss.redialSig <- struct{}{}:
		default:
		}
	}
}

func (ss *session) getH2sess() *h2mux.Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if ss.h2sess == nil || ss.h2sess.IsClosed() {
		return nil
	}
	return ss.h2sess
}

// Dial opens a new stream over the session's active h2mux connection.
func (ss *session) Dial(ctx context.Context) (net.Conn, error) {
	h2sess := ss.getH2sess()
	if h2sess == nil {
		return nil, ErrNoSession
	}
	return h2sess.Open(ctx)
}

// h2Dial dials to the remote, performs TLS, and establishes an HTTP/2 session.
func (ss *session) h2Dial(ctx context.Context) (*h2mux.Session, error) {
	cfg, tlscfg := ss.getConfig()
	if ss.dialAddr == "" {
		return nil, ErrNoDialAddress
	}
	if !ss.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer ss.dialMu.Unlock()
	start := time.Now()
	rawConn, err := ss.s.dialer.DialContext(ctx, network, ss.dialAddr)
	if err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%q => %v", ss.id, rawConn.RemoteAddr())
	if deadline, ok := ctx.Deadline(); ok {
		if err := rawConn.SetDeadline(deadline); err != nil {
			ioClose(rawConn)
			return nil, err
		}
	}
	cfg.SetMuxConnParams(rawConn)
	rawConn = snet.FlowMeter(rawConn, ss.s.flowStats)
	var conn net.Conn = rawConn
	scheme := "https"
	if tlscfg != nil {
		tlsConn := tls.Client(rawConn, tlscfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			ioClose(rawConn)
			return nil, err
		}
		conn = tlsConn
	} else {
		scheme = "http"
		slog.Warningf("%s: connection is not encrypted", tag)
	}

	transport := cfg.NewH2Transport(tlscfg)
	h2conn, err := transport.NewClientConn(conn)
	if err != nil {
		ioClose(conn)
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	localID := cfg.Service.ID
	h2sess, err := h2mux.NewClientSession(ctx, h2conn, ss.dialAddr, scheme, localID, tag)
	if err != nil {
		_ = h2conn.Close()
		return nil, err
	}

	ss.addH2sess(h2sess)
	// When the session closes, trigger a redial.
	if err := ss.s.g.Go(func() {
		defer ss.delH2sess(h2sess)
		<-h2sess.CloseChan()
	}); err != nil {
		ss.delH2sess(h2sess)
		return nil, err
	}

	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	return h2sess, nil
}

// SessionStats holds statistics of a session.
type SessionStats struct {
	Name        string
	LastChanged time.Time
	NumStreams  int
	Active      bool
}

// Stats returns the current statistics of the session.
func (ss *session) Stats() SessionStats {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	active := ss.h2sess != nil && !ss.h2sess.IsClosed()
	numStreams := 0
	if active {
		numStreams = ss.h2sess.NumStreams()
	}
	return SessionStats{
		Name:        ss.id,
		LastChanged: ss.lastChanged,
		NumStreams:  numStreams,
		Active:      active,
	}
}
