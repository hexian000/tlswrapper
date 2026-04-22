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
	"github.com/hexian000/tlswrapper/v3/proto"
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

// Serve handles an incoming connection
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
	req, err := proto.Read(conn)
	if err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	if req.Msg != proto.MsgClientHello {
		slog.Errorf("%s: %s", tag, "invalid message")
		return
	}
	peerID := req.Extensions.Service.ID
	if peerID != "" {
		tag = fmt.Sprintf("%q <= %v", peerID, conn.RemoteAddr())
	}
	rsp := &proto.Message{
		Type: proto.Type,
		Msg:  proto.MsgServerHello,
	}
	rsp.Extensions.Service.ID = cfg.Service.ID
	if err := proto.Write(conn, rsp); err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	h.s.stats.authorized.Add(1)

	_, err = h.s.startMux(conn, cfg, peerID, nil, tag)
	if err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
}

// ForwardHandler forwards connections to the locally configured service address
type ForwardHandler struct {
	s        *Server
	peerName string
}

// Serve handles an incoming connection
func (h *ForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	h.s.stats.request.Add(1)
	peerName := "?"
	if h.peerName != "" {
		peerName = fmt.Sprintf("%q", h.peerName)
	}
	cfg, _ := h.s.getConfig()
	// prefer per-service connect address, fall back to top-level connect
	dialAddr := cfg.ServiceEntry(h.peerName).Connect
	if dialAddr == "" {
		dialAddr = cfg.Connect
	}
	if dialAddr == "" {
		slog.Warningf("tunnel %s: no connect address configured", peerName)
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
