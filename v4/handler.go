// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/forwarder"
	"github.com/hexian000/tlswrapper/v4/mux"
)

// Handler serves one accepted connection.
type Handler interface {
	Serve(context.Context, net.Conn)
}

// MuxHandler upgrades accepted mux sockets into server-side sessions.
type MuxHandler struct {
	s *Server

	halfOpen atomic.Uint32
}

// Stats4Listener reports active sessions and handshakes still in progress.
func (h *MuxHandler) Stats4Listener() (numSessions uint32, numHalfOpen uint32) {
	numSessions = h.s.numSessions.Load()
	numHalfOpen = h.halfOpen.Load()
	return
}

func (h *MuxHandler) Serve(ctx context.Context, conn net.Conn) {
	h.halfOpen.Add(1)
	defer h.halfOpen.Add(^uint32(0))
	start := time.Now()
	cfg, tlscfg := h.s.getConfig()
	tag := formatTunnelTag(false, cfg.Identity.Claim, "", "", conn.LocalAddr(), conn.RemoteAddr(), conn)
	setTCPConnParams(cfg.Mux.TCP, conn)
	conn = snet.FlowMeter(conn, h.s.flowStats)
	if tlscfg == nil {
		slog.Warningf("%s: connection is not encrypted", tag)
	}
	h2cfg := &mux.Config{
		TLSConfig:            tlscfg,
		LocalID:              cfg.Identity.Claim,
		SessionWindow:        int32(cfg.Mux.SessionWindow),
		StreamWindow:         int32(cfg.Mux.StreamWindow),
		MaxConcurrentStreams: uint32(cfg.Mux.MaxHalfOpen),
		IdleTimeout:          cfg.Timeout(),
	}
	ss, err := mux.Server(ctx, conn, h2cfg)
	if err != nil {
		slog.Errorf("%s: %s", tag, formats.Error(err))
		return
	}
	slog.Debugf("%s: setup %v", tag, formats.Duration(time.Since(start)))
	h.s.serveSession(ss)
}

// LocalHandler forwards accepted local connections over a matching mux session.
type LocalHandler struct {
	l  net.Listener
	s  *Server
	id string
}

func (h *LocalHandler) Serve(ctx context.Context, accepted net.Conn) {
	cfg, _ := h.s.getConfig()
	setTCPConnParams(cfg.TCP, accepted)
	t := h.s.findSession(h.id)
	if t == nil {
		tunnelTag := formatTunnelTag(true, cfg.Identity.Claim, "", h.id, accepted.LocalAddr(), nil, accepted)
		slog.Warningf("%s: no active session", tunnelTag)
		ioClose(accepted)
		return
	}
	tunnelTag := t.tagValue()
	peerIdentity := ""
	if sess := t.getSession(); sess != nil {
		peerIdentity = sess.PeerID()
	}
	dialed, err := t.OpenStream(ctx)
	if err != nil {
		slog.Errorf("%s: %s", tunnelTag, formats.Error(err))
		ioClose(accepted)
		return
	}
	seq := t.nextStreamSeq()
	tag := formatStreamTag(seq, true, cfg.Identity.Claim, peerIdentity, h.id, accepted.LocalAddr(), dialed.RemoteAddr(), dialed)
	if err := h.s.f.Start(accepted, dialed, forwarder.HandlerFuncs{
		WriteClosed: func(conn net.Conn, err error) {
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

// EmptyHandler closes every accepted connection.
type EmptyHandler struct{}

func (h *EmptyHandler) Serve(_ context.Context, accepted net.Conn) {
	ioClose(accepted)
}
