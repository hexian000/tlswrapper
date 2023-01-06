package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/slog"
)

func (s *Server) sessionDial(ctx context.Context, addr string, tlscfg *tls.Config) (*Session, error) {
	slog.Info("session dial:", addr)
	startTime := time.Now()
	conn, err := s.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s.cfg.SetConnParams(conn)
	if tlscfg != nil {
		tlsConn := tls.Client(conn, tlscfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = tlsConn.Close()
			return nil, err
		}
		conn = tlsConn
	} else {
		slog.Warning("connection is not encrypted")
	}
	mux, err := yamux.Client(conn, s.muxcfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	slog.Info("session dial:", conn.LocalAddr(), "<->", conn.RemoteAddr(), "setup:", time.Since(startTime))
	name := fmt.Sprintf("%s -> %s", conn.LocalAddr(), conn.RemoteAddr())
	session := s.newSession(name, mux)
	return session, nil
}

type clientSession struct {
	s  *Server
	mu sync.Mutex

	config  *ClientConfig
	tlscfg  *tls.Config
	muxcfg  *yamux.Config
	session *Session
}

func newClientSession(server *Server, tlscfg *tls.Config, config *ClientConfig) *clientSession {
	c := &clientSession{
		s:      server,
		tlscfg: tlscfg,
		muxcfg: server.cfg.NewMuxConfig(false),
		config: config,
	}
	return c
}

func (c *clientSession) sessionDial(ctx context.Context) (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil || c.session.mux.IsClosed() {
		session, err := c.s.sessionDial(ctx, c.config.Dial, c.s.tlscfg)
		if err == nil {
			c.session = session
		}
		return session, err
	}
	return c.session, nil
}

type ClientHandler struct {
	*clientSession
}

func (h *ClientHandler) Serve(ctx context.Context, accepted net.Conn) {
	session, err := h.sessionDial(ctx)
	if err != nil {
		slog.Warningf("dial failed: %s, closing: %v", err, accepted.RemoteAddr())
		_ = accepted.Close()
		return
	}
	dialed, err := session.mux.Open()
	if err != nil {
		slog.Warningf("stream open: %s, closing: %v", err, accepted.RemoteAddr())
		_ = accepted.Close()
		return
	}
	h.s.forward(accepted, dialed)
}
