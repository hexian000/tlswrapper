package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/v2/formats"
	"github.com/hexian000/tlswrapper/v2/hlistener"
	"github.com/hexian000/tlswrapper/v2/meter"
	"github.com/hexian000/tlswrapper/v2/proto"
	"github.com/hexian000/tlswrapper/v2/slog"
)

type Handler interface {
	Serve(context.Context, net.Conn)
}

// TLSHandler creates a tunnel
type TLSHandler struct {
	s *Server
	t *Tunnel

	unauthorized atomic.Uint32
}

func (h *TLSHandler) Stats() hlistener.ServerStats {
	return hlistener.ServerStats{
		Sessions: uint32(h.s.NumSessions()),
		HalfOpen: h.unauthorized.Load(),
	}
}

func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	h.unauthorized.Add(1)
	defer h.unauthorized.Add(^uint32(0))
	start := time.Now()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			slog.Errorf("tunnel %q: accept %v, (%T) %v", h.t.name, conn.RemoteAddr(), err, err)
			return
		}
	}
	c := h.s.getConfig()
	c.SetConnParams(conn)
	conn = meter.Conn(conn, h.s.meter)
	if tlscfg := h.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("tunnel %q: connection is not encrypted", h.t.name)
	}
	handshake := &proto.Handshake{
		Identity: c.Identity,
	}
	if err := proto.RunHandshake(conn, handshake); err != nil {
		slog.Errorf("tunnel %q: accept %v, (%T) %v", h.t.name, conn.RemoteAddr(), err, err)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Server(conn, h.s.getMuxConfig(true))
	if err != nil {
		slog.Errorf("tunnel %q: accept %v, (%T) %v", h.t.name, conn.RemoteAddr(), err, err)
		return
	}
	h.s.stats.authorized.Add(1)
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
	h.s.stats.request.Add(1)
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
	h.s.stats.success.Add(1)
}

// TunnelHandler forwards connections over the tunnel
type TunnelHandler struct {
	s *Server
	t *Tunnel
}

func (h *TunnelHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.t.MuxDial(ctx)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			slog.Debugf("tunnel %q: (%T) %v", h.t.name, err, err)
		} else {
			slog.Errorf("tunnel %q: (%T) %v", h.t.name, err, err)
		}
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

func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	_ = accepted.Close()
}
