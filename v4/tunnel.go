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

type tunnel struct {
	peerName    string // used for logging
	s           *Server
	l           net.Listener
	mu          sync.RWMutex
	mux         map[*yamux.Session]string    // map[mux]tag
	idleSince   map[*yamux.Session]time.Time // tracks when each session became stream-less
	closeSig    chan struct{}
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func newTunnel(peerName string, s *Server) *tunnel {
	return &tunnel{
		peerName: peerName, s: s,
		mux:       make(map[*yamux.Session]string),
		idleSince: make(map[*yamux.Session]time.Time),
		closeSig:  make(chan struct{}),
		redialSig: make(chan struct{}, 1),
	}
}

func (t *tunnel) getConfig() (*config.File, *tls.Config) {
	return t.s.getConfig()
}

// Start starts the tunnel, including listening if configured
func (t *tunnel) Start() error {
	cfg, _ := t.getConfig()
	if listenAddr := cfg.ServiceEntry(t.peerName).Listen; listenAddr != "" {
		l, err := t.s.Listen(listenAddr)
		if err != nil {
			return err
		}
		slog.Noticef("tunnel %q: listen %v", t.peerName, l.Addr())
		h := &TunnelHandler{l: l, s: t.s, t: t}
		if err := t.s.g.Go(func() {
			t.s.Serve(l, h)
		}); err != nil {
			ioClose(l)
			return err
		}
		t.l = l
	}
	slog.Debugf("tunnel %q: start", t.peerName)
	return t.s.g.Go(t.run)
}

// Stop stops the tunnel
func (t *tunnel) Stop() error {
	close(t.closeSig)
	slog.Debugf("tunnel %q: stop", t.peerName)
	return nil
}

func (t *tunnel) cleanMux() {
	cfg, _ := t.getConfig()
	idleTimeout := time.Duration(cfg.IdleTimeout) * time.Second
	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()
	num := len(t.mux)
	for ss := range t.mux {
		if ss.IsClosed() {
			delete(t.mux, ss)
			delete(t.idleSince, ss)
		}
	}
	// update idle tracking
	for ss := range t.mux {
		if ss.NumStreams() == 0 {
			if _, ok := t.idleSince[ss]; !ok {
				t.idleSince[ss] = now
			}
		} else {
			delete(t.idleSince, ss)
		}
	}
	// evict sessions that have been idle longer than IdleTimeout
	if idleTimeout > 0 {
		for ss, since := range t.idleSince {
			if now.Sub(since) >= idleTimeout {
				slog.Debugf("%s: idle session evicted after %v", t.mux[ss], now.Sub(since))
				ioClose(ss)
				delete(t.mux, ss)
				delete(t.idleSince, ss)
			}
		}
	}
	// close redundant idle sessions (keep at most one)
	remain := len(t.mux)
	for ss, tag := range t.mux {
		if remain > 1 && ss.NumStreams() == 0 {
			ioClose(ss)
			delete(t.mux, ss)
			delete(t.idleSince, ss)
			slog.Debugf("%s: closed due to redundancy", tag)
			remain--
		}
	}
	t.s.numSessions.Add(uint32(len(t.mux) - num))
}

func (t *tunnel) redial() {
	ctx := t.s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer t.s.ctx.cancel(ctx)
	_, err := t.muxDial(ctx)
	if err != nil && !errors.Is(err, ErrNoDialAddress) && !errors.Is(err, ErrDialInProgress) {
		redialCount := t.redialCount + 1
		if redialCount > t.redialCount {
			t.redialCount = redialCount
		}
		cfg, _ := t.getConfig()
		slog.Warningf("tunnel %q: redial #%d to %s: %s", t.peerName, t.redialCount, cfg.ServiceEntry(t.peerName).MuxConnect, formats.Error(err))
		return
	}
	t.redialCount = 0
}

func (t *tunnel) maintenance() {
	t.cleanMux()
	n := t.NumSessions()
	if n < 1 {
		cfg, _ := t.getConfig()
		if !cfg.NoRedial && cfg.ServiceEntry(t.peerName).MuxConnect != "" {
			t.redial()
		}
		return
	}
}

func (t *tunnel) schedule() <-chan time.Time {
	cfg, _ := t.getConfig()
	if cfg.NoRedial || cfg.ServiceEntry(t.peerName).MuxConnect == "" || t.redialCount < 1 {
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
	slog.Debugf("tunnel %q: redial scheduled after %v", t.peerName, waitTime)
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
		for mux := range t.mux {
			ioClose(mux)
			delete(t.mux, mux)
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
			// server shutdown
			return
		}
	}
}

// addMux adds a yamux session to the tunnel's mux map
func (t *tunnel) addMux(mux *yamux.Session, tag string) {
	now := time.Now()
	msg := fmt.Sprintf("%s: session established", tag)
	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)

	t.mu.Lock()
	defer t.mu.Unlock()
	num := len(t.mux)
	for mux := range t.mux {
		if mux.IsClosed() {
			delete(t.mux, mux)
		}
	}
	t.mux[mux] = tag
	t.s.numSessions.Add(uint32(len(t.mux) - num))
	t.lastChanged = now
}

func (t *tunnel) delMux(mux *yamux.Session) {
	now := time.Now()
	if tag, ok := func() (string, bool) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		tag, ok := t.mux[mux]
		return tag, ok
	}(); ok {
		msg := fmt.Sprintf("%s: session closed", tag)
		slog.Notice(msg)
		t.s.recentEvents.Add(now, msg)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	num := len(t.mux)
	delete(t.mux, mux)
	for mux := range t.mux {
		if mux.IsClosed() {
			delete(t.mux, mux)
		}
	}
	t.s.numSessions.Add(uint32(len(t.mux) - num))
	t.lastChanged = now
	select {
	case t.redialSig <- struct{}{}:
	default:
	}
}

func (t *tunnel) getMux() *yamux.Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var mux *yamux.Session
	maxNumStreams := 0
	for ss := range t.mux {
		if ss.IsClosed() {
			continue
		}
		numStreams := ss.NumStreams()
		if mux == nil || numStreams > maxNumStreams {
			mux = ss
			maxNumStreams = numStreams
		}
	}
	return mux
}

// NumSessions returns the current number of active yamux sessions
func (t *tunnel) NumSessions() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.mux)
}

// muxDial dials to the remote and establishes a yamux session
func (t *tunnel) muxDial(ctx context.Context) (*yamux.Session, error) {
	cfg, tlscfg := t.getConfig()
	dialAddr := cfg.ServiceEntry(t.peerName).MuxConnect
	if dialAddr == "" {
		return nil, ErrNoDialAddress
	}
	if !t.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer t.dialMu.Unlock()
	start := time.Now()
	conn, err := t.s.dialer.DialContext(ctx, network, dialAddr)
	if err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%q => %v", t.peerName, conn.RemoteAddr())
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	cfg.SetMuxConnParams(conn)
	conn = snet.FlowMeter(conn, t.s.flowStats)
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
		return nil, err
	}
	if rsp.ID != "" && rsp.ID != t.peerName {
		slog.Warningf("%s: peer id mismatch, remote claimed %q", tag, rsp.ID)
	}
	_ = conn.SetDeadline(time.Time{})

	mux, err := t.s.startMux(conn, cfg, rsp.ID, t, tag)
	if err != nil {
		return nil, err
	}
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	return mux, nil
}

// Dial opens a new stream over an existing yamux session, or dials a new session if needed
func (t *tunnel) Dial(ctx context.Context) (net.Conn, error) {
	mux := t.getMux()
	if mux == nil {
		var err error
		if mux, err = t.muxDial(ctx); err != nil {
			return nil, err
		}
	}
	stream, err := mux.OpenStream()
	if err != nil {
		return nil, err
	}
	slog.Debugf("stream open: %q ID=%v", t.peerName, stream.StreamID())
	return stream, nil
}

// TunnelStats holds statistics of a tunnel
type TunnelStats struct {
	Name        string
	LastChanged time.Time
	NumSessions int
	NumStreams  int
}

// Stats returns the current statistics of the tunnel
func (t *tunnel) Stats() TunnelStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	numSessions, numStreams := 0, 0
	for mux := range t.mux {
		if !mux.IsClosed() {
			numSessions++
			numStreams += mux.NumStreams()
		}
	}
	return TunnelStats{
		Name:        t.peerName,
		LastChanged: t.lastChanged,
		NumSessions: numSessions,
		NumStreams:  numStreams,
	}
}
