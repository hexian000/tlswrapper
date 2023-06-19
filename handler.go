package tlswrapper

import (
	"context"
	"crypto/tls"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/meter"
	"github.com/hexian000/tlswrapper/slog"
)

type Handler interface {
	Serve(context.Context, net.Conn)
}

// TLSHandler creates a tunnel
type TLSHandler struct {
	s *Server
	t *Tunnel

	unauthorized uint32
}

func (h *TLSHandler) Unauthorized() uint32 {
	return atomic.LoadUint32(&h.unauthorized)
}

func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	atomic.AddUint32(&h.unauthorized, 1)
	defer atomic.AddUint32(&h.unauthorized, ^uint32(0))
	start := time.Now()
	h.s.c.SetConnParams(conn)
	conn = meter.Conn(conn, h.s.meter)
	if h.s.tlscfg != nil {
		tlsConn := tls.Server(conn, h.s.tlscfg)
		err := tlsConn.HandshakeContext(ctx)
		if err != nil {
			slog.Error(err)
			return
		}
		conn = tlsConn
	} else {
		slog.Warning("connection is not encrypted")
	}
	mux, err := yamux.Server(conn, h.s.servermuxcfg)
	if err != nil {
		slog.Error(err)
		return
	}
	var muxHandler Handler
	if h.t.c.Dial != "" {
		muxHandler = &ForwardHandler{
			h.s,
			h.t.c.Dial,
		}
	} else {
		muxHandler = &EmptyHandler{}
	}
	if err := h.s.g.Go(func() {
		h.s.Serve(mux, muxHandler)
		h.t.onMuxClosed()
	}); err != nil {
		slog.Error(err)
		_ = mux.Close()
		return
	}
	h.t.addMux(mux)
	slog.Info("session accept:", conn.RemoteAddr(), "setup:", time.Since(start))
}

// ForwardHandler forwards connections to another plain address
type ForwardHandler struct {
	s    *Server
	dial string
}

func (h *ForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.s.dialDirect(ctx, h.dial)
	if err != nil {
		_ = accepted.Close()
		slog.Errorf("forward [%s]: %v", h.dial, err)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("forward [%s]: %v", h.dial, err)
		_ = accepted.Close()
		_ = dialed.Close()
		return
	}
}

// TunnelHandler forwards connections over the tunnel
type TunnelHandler struct {
	s *Server
	t *Tunnel
}

func (h *TunnelHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.t.MuxDial(ctx)
	if err != nil {
		slog.Errorf("tunnel [%s]: %s", h.t.name, err)
		_ = accepted.Close()
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("tunnel [%s]: %s", h.t.name, err)
		_ = accepted.Close()
		_ = dialed.Close()
		return
	}
}

// EmptyHandler rejects all connections
type EmptyHandler struct{}

func (h *EmptyHandler) Serve(ctx context.Context, accepted net.Conn) {
	_ = accepted.Close()
}
