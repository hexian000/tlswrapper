// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"net"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/forwarder"
)

// Handler serves one accepted connection.
type Handler interface {
	Serve(context.Context, net.Conn)
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
		peerIdentity = sess.PeerIdentity()
	}
	dialed, err := t.OpenStream(ctx)
	if err != nil {
		slog.Errorf("%s: %s", tunnelTag, formats.Error(err))
		ioClose(accepted)
		return
	}
	tag := formatStreamTag(true, cfg.Identity.Claim, peerIdentity, h.id, accepted.LocalAddr(), dialed.RemoteAddr(), dialed)
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

// identityListener owns a named local listener that routes inbound TCP
// connections to the mux session identified by id.
type identityListener struct {
	id   string
	addr string // configured listen address; compared on config reload
	l    net.Listener
}

// start launches the accept loop for il in s's goroutine group.
func (il *identityListener) start(s *Server) error {
	h := &LocalHandler{s: s, id: il.id}
	return s.g.Go(func() { s.Serve(il.l, h) })
}

// stop closes the listener.
func (il *identityListener) stop() {
	ioClose(il.l)
}
