// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"golang.org/x/net/http2"
)

// ErrSessionClosed is returned by Accept and Open when the session has been closed.
var ErrSessionClosed = errors.New("session closed")

// notifyConn wraps a net.Conn and signals when a Read returns an error (e.g. EOF),
// allowing the caller to detect connection closure without polling.
type notifyConn struct {
	net.Conn
	onReadErr chan struct{}
}

func (c *notifyConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if err != nil {
		select {
		case c.onReadErr <- struct{}{}:
		default:
		}
	}
	return
}

// notifyConnClose wraps conn so that when a Read on the underlying connection
// returns an error (e.g. EOF or network error), the returned channel is signaled.
func notifyConnClose(conn net.Conn) (net.Conn, <-chan struct{}) {
	ch := make(chan struct{}, 1)
	return &notifyConn{Conn: conn, onReadErr: ch}, ch
}

// Session wraps an HTTP/2 connection and provides yamux-style Open/Accept stream
// multiplexing. A Session is either client-mode (returned by Client) or
// server-mode (returned by Server).
type Session struct {
	isServer   bool
	mu         sync.RWMutex
	peerID     string
	tag        string
	localAddr  net.Addr
	remoteAddr net.Addr
	numStreams atomic.Int32
	closedCh   chan struct{}
	closeOnce  sync.Once

	// client mode only
	h2conn   *http2.ClientConn
	dialAddr string
	scheme   string

	// server mode only
	rawConn   net.Conn // underlying connection; Close() closes this to stop ServeConn
	acceptCh  chan net.Conn
	ready     chan struct{}
	helloOnce sync.Once
	helloOK   bool
	localID   string
}

// newClientSession sends a /hello handshake over the already-established h2conn and
// returns a client-mode Session.
func newClientSession(
	ctx context.Context,
	h2conn *http2.ClientConn,
	connCloseCh <-chan struct{},
	dialAddr, scheme, localID, tag string,
) (*Session, error) {
	s := &Session{
		isServer:   false,
		tag:        tag,
		localAddr:  h2Addr{Addr: "local"},
		remoteAddr: h2Addr{Addr: dialAddr},
		closedCh:   make(chan struct{}),
		h2conn:     h2conn,
		dialAddr:   dialAddr,
		scheme:     scheme,
	}

	// Build and send ClientHello via POST /hello.
	helloReq := &message{
		Type: protoType,
		Msg:  msgClientHello,
	}
	if localID != "" {
		helloReq.Extensions.Service = &serviceExt{ID: localID}
	}
	var buf bytes.Buffer
	if err := writeTo(&buf, helloReq); err != nil {
		return nil, err
	}
	helloURL := (&url.URL{Scheme: scheme, Host: dialAddr, Path: "/hello"}).String()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, helloURL, &buf)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", protoType)

	resp, err := h2conn.RoundTrip(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hello: unexpected status %d", resp.StatusCode)
	}
	rsp, err := readFrom(resp.Body)
	if err != nil {
		return nil, err
	}
	if rsp.Extensions.Service != nil {
		s.peerID = rsp.Extensions.Service.ID
	}

	// Detect h2conn closure via the underlying net.Conn's Read error
	// and propagate it to closedCh. No polling needed.
	go func() {
		select {
		case <-connCloseCh:
			_ = s.Close()
		case <-s.closedCh:
		}
	}()

	return s, nil
}

// newServerSession returns a server-mode Session that implements http.Handler.
func newServerSession(localAddr, remoteAddr net.Addr, localID string) *Session {
	return &Session{
		isServer:   true,
		tag:        fmt.Sprintf("? <= %v", remoteAddr),
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		localID:    localID,
		closedCh:   make(chan struct{}),
		acceptCh:   make(chan net.Conn, 16),
		ready:      make(chan struct{}),
	}
}

// ServeHTTP implements http.Handler for server mode.
// It routes /hello (handshake) and /stream (bidirectional streams).
func (s *Session) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/hello":
		s.helloOnce.Do(func() { s.handleHello(w, r) })
	case "/stream":
		s.handleStream(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Session) handleHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		close(s.ready)
		return
	}
	req, err := readFrom(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		close(s.ready)
		return
	}
	if req.Msg != msgClientHello {
		http.Error(w, "unexpected message", http.StatusBadRequest)
		close(s.ready)
		return
	}
	var peerID string
	if req.Extensions.Service != nil {
		peerID = req.Extensions.Service.ID
	}

	rsp := &message{
		Type: protoType,
		Msg:  msgServerHello,
	}
	if s.localID != "" {
		rsp.Extensions.Service = &serviceExt{ID: s.localID}
	}
	w.Header().Set("Content-Type", protoType)
	w.WriteHeader(http.StatusOK)
	if err := writeTo(w, rsp); err != nil {
		close(s.ready)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	s.mu.Lock()
	s.peerID = peerID
	if peerID != "" {
		s.tag = fmt.Sprintf("%q <= %v", peerID, s.remoteAddr)
	}
	s.helloOK = true
	s.mu.Unlock()

	close(s.ready)
}

func (s *Session) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Wait for /hello to complete before accepting streams.
	select {
	case <-s.ready:
	case <-r.Context().Done():
		return
	}
	if !s.helloOK {
		http.Error(w, "not authorized", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()

	// Wrap the response writer + request body as a net.Conn.
	// done is closed when the conn's Close() is called, unblocking this goroutine
	// so that ServeHTTP can return and end the HTTP/2 stream.
	done := make(chan struct{})
	bw := &h2BufFlusher{bufio.NewWriterSize(w, 32*1024), flusher}
	base := newResponseBodyConn(bw, r.Body, s.localAddr, s.remoteAddr)
	conn := &serverStreamConn{
		Conn:       base,
		done:       done,
		numStreams: &s.numStreams,
	}
	s.numStreams.Add(1)

	select {
	case s.acceptCh <- conn:
	case <-s.closedCh:
		s.numStreams.Add(-1)
		return
	}

	// Block here until the stream is closed or the session closes.
	// Returning from ServeHTTP ends the HTTP/2 stream on the wire.
	select {
	case <-done:
	case <-s.closedCh:
	}
}

// Open opens a new bidirectional stream over the session (client mode).
// ctx is used only to detect if the session is already closed; the stream
// itself runs for its entire lifetime independent of ctx.
func (s *Session) Open(ctx context.Context) (net.Conn, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	streamURL := (&url.URL{Scheme: s.scheme, Host: s.dialAddr, Path: "/stream"}).String()
	pr, pw := io.Pipe()
	// Use context.Background() so that cancelling the dial ctx does not cancel
	// the stream after it is established.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, streamURL, pr)
	if err != nil {
		_ = pw.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	s.numStreams.Add(1)
	resp, err := s.h2conn.RoundTrip(req)
	if err != nil {
		s.numStreams.Add(-1)
		_ = pw.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		s.numStreams.Add(-1)
		_ = pw.Close()
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream: unexpected status %d", resp.StatusCode)
	}

	var decOnce sync.Once
	decBody := &onCloseBody{ReadCloser: resp.Body, onClose: func() {
		decOnce.Do(func() { s.numStreams.Add(-1) })
	}}
	return newH2StreamConn(pw, decBody, s.localAddr, s.remoteAddr), nil
}

// Accept returns the next incoming stream (server mode). Blocks until a stream
// arrives or the session is closed.
func (s *Session) Accept() (net.Conn, error) {
	select {
	case conn := <-s.acceptCh:
		return conn, nil
	case <-s.closedCh:
		// Drain one buffered stream if available before reporting closed.
		select {
		case conn := <-s.acceptCh:
			return conn, nil
		default:
			return nil, ErrSessionClosed
		}
	}
}

// PeerID returns the remote service ID received during the handshake.
func (s *Session) PeerID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.peerID
}

// Tag returns the connection tag used for logging.
func (s *Session) Tag() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tag
}

// RemoteAddr returns the remote address of the underlying connection.
func (s *Session) RemoteAddr() net.Addr { return s.remoteAddr }

// NumStreams returns the current number of active streams.
func (s *Session) NumStreams() int { return int(s.numStreams.Load()) }

// IsClosed reports whether the session has been closed.
func (s *Session) IsClosed() bool {
	select {
	case <-s.closedCh:
		return true
	default:
		return false
	}
}

// CloseChan returns a channel that is closed when the session closes.
func (s *Session) CloseChan() <-chan struct{} { return s.closedCh }

// Close shuts down the session. For client mode it closes the underlying
// http2.ClientConn; for server mode it closes the underlying net.Conn (which
// terminates the ServeConn goroutine). Safe to call multiple times.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		close(s.closedCh)
		if s.isServer {
			if s.rawConn != nil {
				_ = s.rawConn.Close()
			}
		} else {
			_ = s.h2conn.Close()
		}
	})
	return nil
}

// serverStreamConn wraps a net.Conn and signals done when closed or half-closed,
// unblocking handleStream's ServeHTTP goroutine so the HTTP/2 stream lifetime
// matches the stream connection lifetime.
type serverStreamConn struct {
	net.Conn
	done       chan struct{}
	doneOnce   sync.Once
	numStreams *atomic.Int32
}

// Close signals done (unblocking ServeHTTP) and then closes the underlying conn
// (releasing the request body). Implements net.Conn.
func (c *serverStreamConn) Close() error {
	c.done_()
	return c.Conn.Close()
}

// CloseWrite signals the end of the write (response) side, unblocking ServeHTTP
// without closing the read (request body) side. Implements forwarder.CloseWriter.
func (c *serverStreamConn) CloseWrite() error {
	c.done_()
	return nil
}

// done_ signals done exactly once, decrementing numStreams and closing the done channel.
func (c *serverStreamConn) done_() {
	c.doneOnce.Do(func() {
		c.numStreams.Add(-1)
		close(c.done)
	})
}

// onCloseBody wraps an io.ReadCloser and calls onClose exactly once on Close.
type onCloseBody struct {
	io.ReadCloser
	onClose func()
}

func (b *onCloseBody) Close() error {
	b.onClose()
	return b.ReadCloser.Close()
}

// h2BufFlusher combines a bufio.Writer with an http.Flusher to implement flushWriter.
type h2BufFlusher struct {
	bw *bufio.Writer
	f  http.Flusher
}

func (bf *h2BufFlusher) Write(p []byte) (int, error) { return bf.bw.Write(p) }

func (bf *h2BufFlusher) Flush() {
	_ = bf.bw.Flush()
	bf.f.Flush()
}
