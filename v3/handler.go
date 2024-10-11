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
			slog.Errorf("? <= %v: %s", conn.RemoteAddr(), formats.Error(err))
			return
		}
	}
	cfg := h.s.getConfig()
	cfg.SetConnParams(conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg := h.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("? <= %v: connection is not encrypted", conn.RemoteAddr())
	}
	req, err := proto.Read(conn)
	if err != nil {
		slog.Errorf("? <= %v: %s", conn.RemoteAddr(), formats.Error(err))
		return
	}
	if req.Msg != proto.MsgClientHello {
		slog.Errorf("? <= %v: %s", conn.RemoteAddr(), "invalid message")
		return
	}
	rsp := &proto.Message{
		Type:     proto.Type,
		Msg:      proto.MsgServerHello,
		PeerName: cfg.PeerName,
	}
	if cfg, ok := cfg.Peers[req.PeerName]; ok {
		rsp.Service = cfg.PeerService
	}
	if err := proto.Write(conn, rsp); err != nil {
		slog.Errorf("%q <= %v: %s", req.PeerName, conn.RemoteAddr(), formats.Error(err))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	h.s.stats.authorized.Add(1)
	t := h.s.findTunnel(req.PeerName)
	var muxcfg *yamux.Config
	if t != nil {
		tuncfg := t.getConfig()
		muxcfg = tuncfg.NewMuxConfig(cfg)
	} else {
		muxcfg = cfg.NewMuxConfig()
	}
	mux, err := yamux.Server(conn, muxcfg)
	if err != nil {
		slog.Errorf("%q <= %v: %s", req.PeerName, conn.RemoteAddr(), formats.Error(err))
		return
	}
	if t != nil {
		t.addMux(mux, false)
	}
	var muxHandler Handler
	if dialAddr := cfg.FindService(req.Service); dialAddr != "" {
		muxHandler = &ForwardHandler{
			s: h.s, tag: req.PeerName, dial: dialAddr,
		}
	}
	if muxHandler == nil {
		if req.Service != "" {
			slog.Infof("%q <= %v: unknown service %q", req.PeerName, conn.RemoteAddr(), rsp.Service)
		}
		if err := mux.GoAway(); err != nil {
			slog.Errorf("%q <= %v: %s", req.PeerName, conn.RemoteAddr(), formats.Error(err))
			return
		}
		muxHandler = &EmptyHandler{}
	}
	if err := h.s.g.Go(func() {
		if t != nil {
			defer t.delMux(mux)
		}
		h.s.Serve(mux, muxHandler)
	}); err != nil {
		slog.Errorf("%q <= %v: %s", req.PeerName, conn.RemoteAddr(), formats.Error(err))
		ioClose(mux)
		return
	}
	slog.Infof("%q <= %v: setup %v", req.PeerName, conn.RemoteAddr(), formats.Duration(time.Since(start)))
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
	t *tunnel
}

func (h *TunnelHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.t.MuxDial(ctx)
	if err != nil {
		if errors.Is(err, ErrDialInProgress) {
			slog.Debugf("%v -> %q: %s", accepted.RemoteAddr(), h.t.peerName, formats.Error(err))
		} else {
			slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.t.peerName, formats.Error(err))
		}
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%v -> %q: %s", accepted.RemoteAddr(), h.t.peerName, formats.Error(err))
		ioClose(accepted)
		ioClose(dialed)
		return
	}
	slog.Debugf("%v -> %q: forward established", h.l.Addr(), h.t.peerName)
}

// EmptyHandler rejects all connections
type EmptyHandler struct{}

func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	ioClose(accepted)
}
