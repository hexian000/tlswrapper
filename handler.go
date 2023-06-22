package tlswrapper

import (
	"context"
	"crypto/tls"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/formats"
	"github.com/hexian000/tlswrapper/meter"
	"github.com/hexian000/tlswrapper/proto"
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
	h.s.getConfig().SetConnParams(conn)
	conn = meter.Conn(conn, h.s.meter)
	if tlscfg := h.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("tunnel %q: connection is not encrypted", h.t.name)
	}
	handshake := &proto.Handshake{
		Identity: h.t.c.Identity,
	}
	if err := proto.RunHandshake(conn, handshake); err != nil {
		slog.Errorf("tunnel %q: accept %v, (%T) %v", h.t.name, conn.RemoteAddr(), err, err)
		return
	}
	mux, err := yamux.Server(conn, h.s.getMuxConfig(true))
	if err != nil {
		slog.Errorf("tunnel %q: accept %v, (%T) %v", h.t.name, conn.RemoteAddr(), err, err)
		return
	}
	tun := h.t
	if handshake.Identity != "" {
		if t := h.s.findTunnel(handshake.Identity); t != nil {
			tun = t
		} else {
			slog.Warningf("unknown remote identity %q", handshake.Identity)
		}
	}
	if err := h.s.g.Go(func() {
		tun.Serve(mux)
	}); err != nil {
		slog.Errorf("tunnel %q: accept %v, (%T) %v", tun.name, conn.RemoteAddr(), err, err)
		_ = mux.Close()
		return
	}
	slog.Infof("tunnel %q: accept %v, setup %v", tun.name, conn.RemoteAddr(), formats.Duration(time.Since(start)))
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
		slog.Errorf("tunnel %q: (%T) %v", h.t.name, err, err)
		_ = accepted.Close()
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("tunnel %q: (%T) %v", h.t.name, err, err)
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
