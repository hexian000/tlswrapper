// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
)

// Handler is a generic interface that handles incoming connections
type Handler interface {
	Serve(context.Context, net.Conn)
}

// TLSHandler creates sessions from incoming TLS connections
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
		tlsConn := tls.Server(conn, tlscfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			slog.Errorf("%s: tls handshake: %s", tag, formats.Error(err))
			return
		}
		conn = tlsConn
	} else {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	_ = conn.SetDeadline(time.Time{})
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	h.s.serveH2Conn(conn)
}

// MuxHandler forwards connections over the session
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
