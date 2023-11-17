package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v2/proto"
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

func (h *TLSHandler) Stats() (numSessions uint32, numHalfOpen uint32) {
	return uint32(h.s.NumSessions()), h.unauthorized.Load()
}

func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	h.unauthorized.Add(1)
	defer h.unauthorized.Add(^uint32(0))
	start := time.Now()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			slog.Errorf("%q <= %v: %s", h.t.name, conn.RemoteAddr(), formats.Error(err))
			return
		}
	}
	c := h.s.getConfig()
	c.SetConnParams(conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg := h.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("%q <= %v: connection is not encrypted", h.t.name, conn.RemoteAddr())
	}
	handshake := &proto.Handshake{
		Identity: c.Identity,
	}
	if err := proto.RunHandshake(conn, handshake); err != nil {
		slog.Errorf("%q <= %v: %s", h.t.name, conn.RemoteAddr(), formats.Error(err))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Server(conn, h.s.getMuxConfig(true))
	if err != nil {
		slog.Errorf("%q <= %v: %s", h.t.name, conn.RemoteAddr(), formats.Error(err))
		return
	}
	h.s.stats.authorized.Add(1)
	tun := h.t
	if handshake.Identity != "" {
		if t := h.s.findTunnel(handshake.Identity); t != nil {
			tun = t
		} else {
			slog.Warningf("%q <= %v: unknown identity %q", tun.name, conn.RemoteAddr(), handshake.Identity)
		}
	}
	if err := h.s.g.Go(func() {
		tun.Serve(mux)
	}); err != nil {
		slog.Errorf("%q <= %v: %s", tun.name, conn.RemoteAddr(), formats.Error(err))
		ioClose(mux)
		return
	}
	slog.Infof("%q <= %v: setup %v", tun.name, conn.RemoteAddr(), formats.Duration(time.Since(start)))
}

// ForwardHandler forwards connections to another plain address
type ForwardHandler struct {
	s    *Server
	name string
	dial string
}

func (h *ForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	h.s.stats.request.Add(1)
	dialed, err := h.s.dialDirect(ctx, h.dial)
	if err != nil {
		slog.Errorf("%q -> %s: %v", h.name, h.dial, err)
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%q -> %s: %v", h.name, h.dial, err)
		ioClose(accepted)
		ioClose(dialed)
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
			slog.Debugf("%v -> %q: %s", accepted.RemoteAddr(), h.t.name, formats.Error(err))
		} else {
			slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.t.name, formats.Error(err))
		}
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.t.name, formats.Error(err))
		ioClose(accepted)
		ioClose(dialed)
		return
	}
}

// EmptyHandler rejects all connections
type EmptyHandler struct{}

func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	ioClose(accepted)
}
