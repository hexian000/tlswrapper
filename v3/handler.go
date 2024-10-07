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
	"github.com/hexian000/tlswrapper/v3/proto"
)

type Handler interface {
	Serve(context.Context, net.Conn)
}

// TLSHandler creates a tunnel
type TLSHandler struct {
	s *Server
	t *Tunnel

	halfOpen atomic.Uint32
}

func (h *TLSHandler) Stats4Listener() (numSessions uint32, numHalfOpen uint32) {
	numSessions = h.s.numSessions.Load()
	numHalfOpen = h.halfOpen.Load()
	return
}

func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	h.halfOpen.Add(1)
	defer h.halfOpen.Add(^uint32(0))
	start := time.Now()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			slog.Errorf("%q <= %v: %s", h.t.tag, conn.RemoteAddr(), formats.Error(err))
			return
		}
	}
	c := h.s.getConfig()
	c.SetConnParams(conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg := h.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("%q <= %v: connection is not encrypted", h.t.tag, conn.RemoteAddr())
	}
	t := h.t
	req := &proto.ServerHello{
		Type:    proto.Type,
		Service: c.RemoteService,
	}
	if t.c.RemoteService != "" {
		req.Service = t.c.RemoteService
	}
	rsp, err := proto.Server(conn, req)
	if err != nil {
		slog.Errorf("%q <= %v: %s", h.t.tag, conn.RemoteAddr(), formats.Error(err))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Server(conn, h.s.getMuxConfig(true))
	if err != nil {
		slog.Errorf("%q <= %v: %s", h.t.tag, conn.RemoteAddr(), formats.Error(err))
		return
	}
	h.s.stats.authorized.Add(1)
	if rsp.Service != "" {
		if tun := h.s.findTunnel(rsp.Service); tun != nil {
			t = tun
		} else {
			slog.Infof("%q <= %v: unknown service %q", t.tag, conn.RemoteAddr(), req.Service)
		}
	}
	t.addMux(mux, false)
	if err := h.s.g.Go(func() {
		defer t.delMux(mux)
		t.Serve(mux)
	}); err != nil {
		slog.Errorf("%q <= %v: %s", t.tag, conn.RemoteAddr(), formats.Error(err))
		ioClose(mux)
		return
	}
	slog.Infof("%q <= %v: setup %v", t.tag, conn.RemoteAddr(), formats.Duration(time.Since(start)))
}

// ForwardHandler forwards connections to another plain address
type ForwardHandler struct {
	s    *Server
	tag  string
	dial string
}

func (h *ForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	h.s.stats.request.Add(1)
	dialed, err := h.s.dialDirect(ctx, h.dial)
	if err != nil {
		slog.Errorf("%q -> %s: %v", h.tag, h.dial, err)
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%q -> %s: %v", h.tag, h.dial, err)
		ioClose(accepted)
		ioClose(dialed)
		return
	}
	slog.Debugf("%q -> %v: forward established", h.tag, dialed.RemoteAddr())
	h.s.stats.success.Add(1)
}

// TunnelHandler forwards connections over the tunnel
type TunnelHandler struct {
	l net.Listener
	s *Server
	t *Tunnel
}

func (h *TunnelHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.t.MuxDial(ctx)
	if err != nil {
		if errors.Is(err, ErrDialInProgress) {
			slog.Debugf("%v -> %q: %s", accepted.RemoteAddr(), h.t.tag, formats.Error(err))
		} else {
			slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.t.tag, formats.Error(err))
		}
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.t.tag, formats.Error(err))
		ioClose(accepted)
		ioClose(dialed)
		return
	}
	slog.Debugf("%v -> %q: forward established", h.l.Addr(), h.t.tag)
}

// EmptyHandler rejects all connections
type EmptyHandler struct{}

func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	ioClose(accepted)
}
