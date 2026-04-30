// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/forwarder"
	"github.com/hexian000/tlswrapper/v4/mux"
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
	cfg, tlscfg := h.s.getConfig()
	cfg.SetMuxConnParams(conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg == nil {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	h2cfg := &mux.Config{
		TLSConfig:            tlscfg,
		LocalID:              cfg.Service.ID,
		SessionWindow:        int32(cfg.Mux.SessionWindow),
		StreamWindow:         int32(cfg.Mux.StreamWindow),
		MaxConcurrentStreams: uint32(cfg.Mux.MaxHalfOpen),
		IdleTimeout:          cfg.Timeout(),
	}
	h2sess, err := mux.Server(ctx, conn, h2cfg)
	if err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	h.s.serveH2Conn(h2sess)
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
	tag := fmt.Sprintf("%v -> %q", accepted.RemoteAddr(), h.id)
	if err := h.s.f.Start(accepted, dialed, forwarder.HandlerFuncs{
		HalfClosed: func(conn net.Conn, err error) {
			if err != nil {
				slog.Debugf("%s: half-close %v: %s", tag, conn.RemoteAddr(), formats.Error(err))
			} else {
				slog.Debugf("%s: half-close %v", tag, conn.RemoteAddr())
			}
		},
		Closed: func() {
			slog.Debugf("%s: forward finished", tag)
		},
	}); err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		ioClose(accepted)
		ioClose(dialed)
		return
	}
	slog.Debugf("%s: forward established", tag)
}

// EmptyHandler rejects all connections
type EmptyHandler struct{}

// Serve handles an incoming connection
func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	ioClose(accepted)
}
