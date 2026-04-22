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

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3/config"
	"github.com/hexian000/tlswrapper/v3/proto"
)

// session wraps exactly one yamux.Session.
// Inbound sessions (accepted from a TCP listener) have dialAddr == "".
// Outbound sessions (dialled by MuxConnect) own a redial loop in run().
type session struct {
	id       string // used for logging and lookup
	dialAddr string // MuxConnect address; empty for inbound sessions
	s        *Server
	l        net.Listener // local TCP listener (only on config-driven sessions with Listen)

	mu          sync.RWMutex
	mux         *yamux.Session
	tag         string    // connection tag for the current mux (set in addMux)
	idleSince   time.Time // when mux became stream-less (zero = not idle)
	closeSig    chan struct{}
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func newSession(peerName, dialAddr string, s *Server) *session {
	return &session{
		id:        peerName,
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

func (ss *session) cleanMux() {
	cfg, _ := ss.getConfig()
	idleTimeout := time.Duration(cfg.IdleTimeout) * time.Second
	now := time.Now()

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.mux == nil {
		return
	}
	if ss.mux.IsClosed() {
		ss.mux = nil
		ss.idleSince = time.Time{}
		return
	}
	// update idle tracking
	if ss.mux.NumStreams() == 0 {
		if ss.idleSince.IsZero() {
			ss.idleSince = now
		}
	} else {
		ss.idleSince = time.Time{}
	}
	// evict if idle too long
	if idleTimeout > 0 && !ss.idleSince.IsZero() && now.Sub(ss.idleSince) >= idleTimeout {
		slog.Debugf("session %q: idle session evicted after %v", ss.id, now.Sub(ss.idleSince))
		ioClose(ss.mux)
		ss.mux = nil
		ss.idleSince = time.Time{}
	}
}

func (ss *session) redial() {
	ctx := ss.s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer ss.s.ctx.cancel(ctx)
	_, err := ss.muxDial(ctx)
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
	ss.cleanMux()
	if ss.getMux() == nil {
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
		if ss.mux != nil {
			ioClose(ss.mux)
			ss.mux = nil
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
			// server shutdown
			return
		}
	}
}

// addMux records an established yamux session on this session object.
func (ss *session) addMux(mux *yamux.Session, tag string) {
	now := time.Now()
	msg := fmt.Sprintf("%s: session established", tag)
	slog.Notice(msg)
	ss.s.recentEvents.Add(now, msg)

	ss.mu.Lock()
	defer ss.mu.Unlock()
	hadMux := ss.mux != nil && !ss.mux.IsClosed()
	ss.mux = mux
	ss.tag = tag
	if !hadMux {
		ss.s.numSessions.Add(1)
	}
	ss.lastChanged = now
}

func (ss *session) delMux(mux *yamux.Session) {
	now := time.Now()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.mux != mux {
		return
	}
	msg := fmt.Sprintf("%s: session closed", ss.tag)
	slog.Notice(msg)
	ss.s.recentEvents.Add(now, msg)
	ss.mux = nil
	ss.tag = ""
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

// getMux returns the active yamux.Session, or nil if there is none.
func (ss *session) getMux() *yamux.Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if ss.mux == nil || ss.mux.IsClosed() {
		return nil
	}
	return ss.mux
}

// Dial opens a new stream over the session's yamux session.
// Returns an error if the session has no active mux yet.
func (ss *session) Dial(ctx context.Context) (net.Conn, error) {
	mux := ss.getMux()
	if mux == nil {
		return nil, ErrNoSession
	}
	stream, err := mux.OpenStream()
	if err != nil {
		return nil, err
	}
	slog.Debugf("stream open: %q ID=%v", ss.id, stream.StreamID())
	return stream, nil
}

// muxDial dials to the remote and establishes a yamux session.
func (ss *session) muxDial(ctx context.Context) (*yamux.Session, error) {
	cfg, tlscfg := ss.getConfig()
	if ss.dialAddr == "" {
		return nil, ErrNoDialAddress
	}
	if !ss.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer ss.dialMu.Unlock()
	start := time.Now()
	conn, err := ss.s.dialer.DialContext(ctx, network, ss.dialAddr)
	if err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%q => %v", ss.id, conn.RemoteAddr())
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			ioClose(conn)
			return nil, err
		}
	}
	cfg.SetMuxConnParams(conn)
	conn = snet.FlowMeter(conn, ss.s.flowStats)
	if tlscfg != nil {
		conn = tls.Client(conn, tlscfg)
	} else {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	req := &proto.Message{
		Type: proto.Type,
		Msg:  proto.MsgClientHello,
		ID:   cfg.ID,
	}
	rsp, err := proto.Roundtrip(conn, req)
	if err != nil {
		ioClose(conn)
		return nil, err
	}
	if rsp.ID != "" && rsp.ID != ss.id {
		slog.Warningf("%s: peer id mismatch, remote claimed %q", tag, rsp.ID)
	}
	_ = conn.SetDeadline(time.Time{})

	mux, err := ss.s.startMux(conn, cfg, rsp.ID, ss, tag)
	if err != nil {
		return nil, err
	}
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	return mux, nil
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
	numStreams := 0
	active := false
	if ss.mux != nil && !ss.mux.IsClosed() {
		active = true
		numStreams = ss.mux.NumStreams()
	}
	return SessionStats{
		Name:        ss.id,
		LastChanged: ss.lastChanged,
		NumStreams:  numStreams,
		Active:      active,
	}
}

// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
