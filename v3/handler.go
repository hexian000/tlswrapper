package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

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
	tag := fmt.Sprintf("? <= %v", conn.RemoteAddr())
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			slog.Errorf("%s: %s", tag, formats.Error(err))
			return
		}
	}
	cfg, tlscfg := h.s.getConfig()
	cfg.SetConnParams(conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	} else {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	req, err := proto.Read(conn)
	if err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	if req.Msg != proto.MsgClientHello {
		slog.Errorf("%s: %s", tag, "invalid message")
		return
	}
	if req.PeerName != "" {
		tag = fmt.Sprintf("%q <= %v", req.PeerName, conn.RemoteAddr())
	}
	rsp := &proto.Message{
		Type:     proto.Type,
		Msg:      proto.MsgServerHello,
		PeerName: cfg.PeerName,
	}
	if cfg, ok := cfg.Peers[req.PeerName]; ok {
		rsp.Service = cfg.Service
	}
	if err := proto.Write(conn, rsp); err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	h.s.stats.authorized.Add(1)

	_, err = h.s.startMux(conn, cfg, req.PeerName, req.Service, nil, tag)
	if err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	slog.Debugf("%s: service=%q, setup %v", tag, req.Service, formats.Duration(time.Since(start)))
}

// ForwardHandler forwards connections to another plain address
type ForwardHandler struct {
	s        *Server
	peerName string
	service  string
}

func (h *ForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	h.s.stats.request.Add(1)
	peerName := "?"
	if h.peerName != "" {
		peerName = fmt.Sprintf("%q", h.peerName)
	}
	cfg, _ := h.s.getConfig()
	dialAddr, ok := cfg.Services[h.service]
	if !ok {
		slog.Warningf("tunnel %s: unknown service %q", peerName, h.service)
		ioClose(accepted)
		return
	}
	tag := peerName + " -> " + dialAddr
	dialed, err := h.s.dialDirect(ctx, dialAddr)
	if err != nil {
		slog.Errorf("%s: %v", tag, err)
		ioClose(accepted)
		return
	}
	if err := h.s.f.Forward(accepted, dialed); err != nil {
		slog.Errorf("%s: %v", tag, err)
		ioClose(accepted)
		ioClose(dialed)
		return
	}
	slog.Debugf("%s: forward established", tag)
	h.s.stats.success.Add(1)
}

// TunnelHandler forwards connections over the tunnel
type TunnelHandler struct {
	l net.Listener
	s *Server
	t *tunnel
}

func (h *TunnelHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.t.Dial(ctx)
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
