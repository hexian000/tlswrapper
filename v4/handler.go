// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/proto"
	"golang.org/x/net/http2"
)

// Handler is a generic interface that handles incoming connections
type Handler interface {
	Serve(context.Context, net.Conn)
}

// TLSHandler creates tunnels from incoming TLS connections
type TLSHandler struct {
	s *Server

	halfOpen atomic.Uint32
}

// Stats4Listener returns the current number of sessions and half-open connections
func (h *TLSHandler) Stats4Listener() (numSessions uint32, numHalfOpen uint32) {
	numSessions = h.s.numSessions.Load()
	numHalfOpen = h.halfOpen.Load()
	return
}

// Serve handles an incoming connection by upgrading it to HTTP/2 and serving it.
func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	h.halfOpen.Add(1)
	defer h.halfOpen.Add(^uint32(0))
	start := time.Now()
	tag := fmt.Sprintf("? <= %v", conn.RemoteAddr())
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			slog.Errorf("%s: %s", tag, formats.Error(err))
			return
		}
	}
	cfg, tlscfg := h.s.getConfig()
	cfg.SetMuxConnParams(conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	_ = conn.SetDeadline(time.Time{})
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	h.s.serveH2Conn(conn, tag)
}

// h2ConnHandler is an http.Handler that handles one HTTP/2 connection (server side).
// It routes /hello (JSON handshake) and /tunnel (bidirectional stream) paths.
type h2ConnHandler struct {
	s       *Server
	tag     string
	peerID  string
	inbound *session
	ready   chan struct{} // closed after /hello completes
	once    sync.Once     // ensures /hello is only accepted once
}

// ServeHTTP routes requests to handleHello or handleTunnel.
func (h *h2ConnHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/hello":
		h.once.Do(func() { h.handleHello(w, r) })
	case "/tunnel":
		h.handleTunnel(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *h2ConnHandler) handleHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		close(h.ready)
		return
	}
	req, err := proto.ReadFrom(r.Body)
	if err != nil {
		slog.Errorf("%s: hello read: %s", h.tag, formats.Error(err))
		http.Error(w, formats.Error(err), http.StatusBadRequest)
		close(h.ready)
		return
	}
	if req.Msg != proto.MsgClientHello {
		slog.Errorf("%s: hello: unexpected msgid %d", h.tag, req.Msg)
		http.Error(w, "unexpected message", http.StatusBadRequest)
		close(h.ready)
		return
	}
	peerID := req.Extensions.Service.ID
	if peerID != "" {
		h.tag = fmt.Sprintf("%q <= %v", peerID, r.RemoteAddr)
	}
	h.peerID = peerID

	cfg, _ := h.s.getConfig()
	rsp := &proto.Message{
		Type: proto.Type,
		Msg:  proto.MsgServerHello,
	}
	rsp.Extensions.Service.ID = cfg.Service.ID

	w.Header().Set("Content-Type", proto.Type)
	w.WriteHeader(http.StatusOK)
	if err := proto.WriteTo(w, rsp); err != nil {
		slog.Errorf("%s: hello write: %s", h.tag, formats.Error(err))
		close(h.ready)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// update inbound session with peer identity
	h.inbound.mu.Lock()
	h.inbound.id = peerID
	h.inbound.lastChanged = time.Now()
	h.inbound.mu.Unlock()

	now := time.Now()
	msg := fmt.Sprintf("%s: session established", h.tag)
	slog.Notice(msg)
	h.s.recentEvents.Add(now, msg)
	h.inbound.mu.Lock()
	if h.inbound.h2conn == nil {
		h.s.numSessions.Add(1)
	}
	h.inbound.lastChanged = now
	h.inbound.mu.Unlock()
	h.s.stats.authorized.Add(1)

	close(h.ready)
	slog.Debugf("%s: hello done", h.tag)
}

func (h *h2ConnHandler) handleTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Wait until /hello completes (or connection closes)
	select {
	case <-h.ready:
	case <-r.Context().Done():
		return
	}
	if h.peerID == "" && h.inbound.id == "" {
		// hello failed
		http.Error(w, "not authorized", http.StatusForbidden)
		return
	}

	h.s.stats.request.Add(1)
	peerName := h.inbound.id
	cfg, _ := h.s.getConfig()
	dialAddr := cfg.ServiceEntry(peerName).Connect
	if dialAddr == "" {
		dialAddr = cfg.Connect
	}
	if dialAddr == "" {
		peerDisplay := "?"
		if peerName != "" {
			peerDisplay = fmt.Sprintf("%q", peerName)
		}
		slog.Warningf("tunnel %s: no connect address configured", peerDisplay)
		http.Error(w, "no connect address", http.StatusServiceUnavailable)
		return
	}
	tag := fmt.Sprintf("%q -> %s", peerName, dialAddr)

	ctx := r.Context()
	dialed, err := h.s.dialDirect(ctx, dialAddr)
	if err != nil {
		slog.Errorf("%s: %v", tag, err)
		http.Error(w, formats.Error(err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	local := h2Addr{"server"}
	remote := h2Addr{r.RemoteAddr}
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Errorf("%s: ResponseWriter does not implement Flusher", tag)
		return
	}
	bw := &bufioFlusher{bufio.NewWriterSize(w, 32*1024), flusher}
	serverConn := newResponseBodyConn(bw, r.Body, local, remote)

	if err := h.s.f.ForwardSync(serverConn, dialed); err != nil {
		slog.Errorf("%s: %v", tag, err)
		return
	}
	slog.Debugf("%s: tunnel done", tag)
	h.s.stats.success.Add(1)
}

// bufioFlusher wraps a bufio.Writer and the underlying http.Flusher together.
type bufioFlusher struct {
	bw *bufio.Writer
	f  http.Flusher
}

func (bf *bufioFlusher) Write(p []byte) (int, error) {
	return bf.bw.Write(p)
}

func (bf *bufioFlusher) Flush() {
	_ = bf.bw.Flush()
	bf.f.Flush()
}

// MuxHandler forwards connections over the tunnel
type MuxHandler struct {
	l  net.Listener
	s  *Server
	id string
}

// Serve handles an incoming connection
func (h *MuxHandler) Serve(ctx context.Context, accepted net.Conn) {
	cfg, _ := h.s.getConfig()
	cfg.SetTCPConnParams(accepted)
	ss := h.s.findSession(h.id)
	if ss == nil {
		slog.Warningf("%v -> %q: no active session", accepted.RemoteAddr(), h.id)
		ioClose(accepted)
		return
	}
	dialed, err := ss.Dial(ctx)
	if err != nil {
		slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.id, formats.Error(err))
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.id, formats.Error(err))
		ioClose(accepted)
		ioClose(dialed)
		return
	}
	slog.Debugf("%v -> %q: forward established", h.l.Addr(), h.id)
}

// EmptyHandler rejects all connections
type EmptyHandler struct{}

// Serve handles an incoming connection
func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	ioClose(accepted)
}

// serveH2ConnHandler is a helper used in serveH2Conn; kept here for clarity.
func newH2ConnHandler(s *Server, inbound *session, tag string) *h2ConnHandler {
	return &h2ConnHandler{
		s:       s,
		tag:     tag,
		inbound: inbound,
		ready:   make(chan struct{}),
	}
}

// configureH2Server sets up the HTTP/2 server for a connection.
func configureH2Server(s *http2.Server, conn net.Conn, h http.Handler) {
	s.ServeConn(conn, &http2.ServeConnOpts{Handler: h})
}
