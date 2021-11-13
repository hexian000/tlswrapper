package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/proxy"
	"github.com/hexian000/tlswrapper/slog"
)

var errShutdown = errors.New("server is shutting down")

func (s *Server) dialTLS(ctx context.Context, addr string, tlscfg *tls.Config) (*Session, error) {
	slog.Verbose("dial TLS:", addr)
	startTime := time.Now()
	conn, err := s.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s.cfg.SetConnParams(conn)
	meteredConn := Meter(conn)
	tlsConn := tls.Client(meteredConn, tlscfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	session, err := yamux.Client(tlsConn, s.muxcfg)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	slog.Info("dial session:", conn.LocalAddr(), "<->", conn.RemoteAddr(), "setup:", time.Since(startTime))
	sessionName := fmt.Sprintf("%s -> %s", conn.LocalAddr(), conn.RemoteAddr())
	info := s.newSession(sessionName, session, meteredConn)
	return info, nil
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
	return &clientSession{
		s:      server,
		tlscfg: tlscfg,
		muxcfg: server.cfg.NewMuxConfig(false),
		config: config,
	}
}

func (c *clientSession) proxyDial(ctx context.Context, addr string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, err
	}
	dialed, err := c.dialMux(ctx)
	if err != nil {
		return nil, err
	}
	slog.Verbose("proxy dial:", addr)
	conn := proxy.Client(dialed, addr)
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *clientSession) dialTLS(ctx context.Context) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.s.shutdownCh:
		return errShutdown
	default:
	}
	if c.session == nil || c.session.mux.IsClosed() {
		c.session, err = c.s.dialTLS(ctx, c.config.Dial, c.s.tlscfg)
	}
	return
}

func (c *clientSession) dialMux(ctx context.Context) (net.Conn, error) {
	err := c.dialTLS(ctx)
	if err != nil {
		slog.Error("dial TLS:", err)
		return nil, err
	}
	dialed, err := c.session.mux.Open()
	if err != nil {
		slog.Error("dial mux:", err)
		_ = c.session.mux.Close()
		return nil, err
	}
	return dialed, nil
}

type ClientProxyHandler struct {
	*clientSession
	config *ForwardConfig
}

func (c *ClientProxyHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := c.proxyDial(ctx, c.config.Forward)
	if err != nil {
		_ = accepted.Close()
		return
	}
	c.s.forward(accepted, dialed)
}

type ClientForwardHandler struct {
	*clientSession
}

func (h *ClientForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.dialMux(ctx)
	if err != nil {
		_ = accepted.Close()
		return
	}
	h.s.forward(accepted, dialed)
}
