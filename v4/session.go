// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/h2mux"
	"golang.org/x/net/http2"
)

// session wraps exactly one HTTP/2 ClientConn.
// Inbound sessions (accepted from a TCP listener) have dialAddr == "".
// Outbound sessions (dialled by MuxConnect) own a redial loop in run().
type session struct {
	id       string // peer service ID; used for logging and lookup
	dialAddr string // MuxConnect address; empty for inbound sessions
	s        *Server
	l        net.Listener // local TCP listener (only on config-driven sessions with Listen)

	mu          sync.RWMutex
	h2conn      *http2.ClientConn
	tag         string // connection tag for the current h2conn
	numStreams  atomic.Int32
	idleSince   time.Time // when h2conn became stream-less (zero = not idle)
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

func (ss *session) cleanH2conn() {
	cfg, _ := ss.getConfig()
	idleTimeout := time.Duration(cfg.IdleTimeout) * time.Second
	now := time.Now()

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.h2conn == nil {
		return
	}
	state := ss.h2conn.State()
	if state.Closed {
		ss.h2conn = nil
		ss.idleSince = time.Time{}
		return
	}
	// update idle tracking
	if ss.numStreams.Load() == 0 {
		if ss.idleSince.IsZero() {
			ss.idleSince = now
		}
	} else {
		ss.idleSince = time.Time{}
	}
	// evict if idle too long
	if idleTimeout > 0 && !ss.idleSince.IsZero() && now.Sub(ss.idleSince) >= idleTimeout {
		slog.Debugf("session %q: idle session evicted after %v", ss.id, now.Sub(ss.idleSince))
		_ = ss.h2conn.Close()
		ss.h2conn = nil
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
	ss.cleanH2conn()
	if ss.getH2conn() == nil {
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
		if ss.h2conn != nil {
			_ = ss.h2conn.Close()
			ss.h2conn = nil
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

// addH2conn records an established HTTP/2 ClientConn on this session object.
func (ss *session) addH2conn(h2conn *http2.ClientConn, tag string) {
	now := time.Now()
	msg := fmt.Sprintf("%s: session established", tag)
	slog.Notice(msg)
	ss.s.recentEvents.Add(now, msg)

	ss.mu.Lock()
	defer ss.mu.Unlock()
	hadConn := ss.h2conn != nil && !ss.h2conn.State().Closed
	ss.h2conn = h2conn
	ss.tag = tag
	if !hadConn {
		ss.s.numSessions.Add(1)
	}
	ss.lastChanged = now
}

func (ss *session) delH2conn(h2conn *http2.ClientConn) {
	now := time.Now()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.h2conn != h2conn {
		return
	}
	msg := fmt.Sprintf("%s: session closed", ss.tag)
	slog.Notice(msg)
	ss.s.recentEvents.Add(now, msg)
	ss.h2conn = nil
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

// getH2conn returns the active http2.ClientConn, or nil if there is none.
func (ss *session) getH2conn() *http2.ClientConn {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if ss.h2conn == nil || ss.h2conn.State().Closed {
		return nil
	}
	return ss.h2conn
}

// Dial opens a new HTTP/2 stream (POST /stream) over the session's connection.
// Returns a net.Conn backed by the request/response body pair.
func (ss *session) Dial(ctx context.Context) (net.Conn, error) {
	h2conn := ss.getH2conn()
	if h2conn == nil {
		return nil, ErrNoSession
	}
	cfg, _ := ss.getConfig()
	dialAddr := cfg.ServiceEntry(ss.id).MuxConnect
	if dialAddr == "" {
		dialAddr = ss.dialAddr
	}
	remoteAddr := h2mux.H2Addr{Addr: dialAddr}
	localAddr := h2mux.H2Addr{Addr: "local"}

	pr, pw := io.Pipe()
	scheme := "https"
	if _, tlscfg := ss.getConfig(); tlscfg == nil {
		scheme = "http"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		(&url.URL{Scheme: scheme, Host: dialAddr, Path: "/tunnel"}).String(), pr)
	if err != nil {
		_ = pw.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	ss.numStreams.Add(1)
	resp, err := h2conn.RoundTrip(req)
	if err != nil {
		ss.numStreams.Add(-1)
		_ = pw.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		ss.numStreams.Add(-1)
		_ = pw.Close()
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream: unexpected status %d", resp.StatusCode)
	}

	var decremented bool
	var decOnce sync.Once
	decBody := &onCloseBody{ReadCloser: resp.Body, onClose: func() {
		decOnce.Do(func() {
			decremented = true
			ss.numStreams.Add(-1)
		})
	}}
	_ = decremented
	tc := h2mux.NewH2StreamConn(pw, decBody, localAddr, remoteAddr)
	return tc, nil
}

// onCloseBody wraps an io.ReadCloser and calls onClose when Close is called.
type onCloseBody struct {
	io.ReadCloser
	onClose func()
}

func (b *onCloseBody) Close() error {
	b.onClose()
	return b.ReadCloser.Close()
}

// h2Dial dials to the remote and establishes an HTTP/2 session.
func (ss *session) h2Dial(ctx context.Context) (*http2.ClientConn, error) {
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
	if tlscfg != nil {
		tlsConn := tls.Client(rawConn, tlscfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			ioClose(rawConn)
			return nil, err
		}
		conn = tlsConn
	} else {
		slog.Warningf("%s: connection is not encrypted", tag)
	}

	transport := cfg.NewH2Transport(tlscfg)
	h2conn, err := transport.NewClientConn(conn)
	if err != nil {
		ioClose(conn)
		return nil, err
	}

	// Send ClientHello via POST /hello
	scheme := "https"
	if tlscfg == nil {
		scheme = "http"
	}
	helloURL := (&url.URL{Scheme: scheme, Host: ss.dialAddr, Path: "/hello"}).String()
	helloReq := &h2mux.Message{
		Type: h2mux.Type,
		Msg:  h2mux.MsgClientHello,
	}
	if cfg.Service.ID != "" {
		helloReq.Extensions.Service = &h2mux.ServiceExt{ID: cfg.Service.ID}
	}

	var buf bytes.Buffer
	if err := h2mux.WriteTo(&buf, helloReq); err != nil {
		_ = h2conn.Close()
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, helloURL, &buf)
	if err != nil {
		_ = h2conn.Close()
		return nil, err
	}
	hreq.Header.Set("Content-Type", h2mux.Type)

	resp, err := h2conn.RoundTrip(hreq)
	if err != nil {
		_ = h2conn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = h2conn.Close()
		return nil, fmt.Errorf("hello: unexpected status %d", resp.StatusCode)
	}
	rsp, err := h2mux.ReadFrom(resp.Body)
	if err != nil {
		_ = h2conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	var rspID string
	if rsp.Extensions.Service != nil {
		rspID = rsp.Extensions.Service.ID
	}
	if rspID != "" && rspID != ss.id {
		slog.Warningf("%s: peer id mismatch, remote claimed %q", tag, rspID)
	}

	ss.addH2conn(h2conn, tag)
	// Monitor the connection; clean up when closed
	if err := ss.s.g.Go(func() {
		defer ss.delH2conn(h2conn)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if h2conn.State().Closed {
					return
				}
			case <-ss.closeSig:
				_ = h2conn.Close()
				return
			case <-ss.s.g.CloseC():
				_ = h2conn.Close()
				return
			}
		}
	}); err != nil {
		ss.delH2conn(h2conn)
		return nil, err
	}

	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	return h2conn, nil
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
	active := ss.h2conn != nil && !ss.h2conn.State().Closed
	numStreams := 0
	if active {
		numStreams = int(ss.numStreams.Load())
	}
	return SessionStats{
		Name:        ss.id,
		LastChanged: ss.lastChanged,
		NumStreams:  numStreams,
		Active:      active,
	}
}
