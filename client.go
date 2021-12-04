package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
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

	apiClient *http.Client
}

func newClientSession(server *Server, tlscfg *tls.Config, config *ClientConfig) *clientSession {
	c := &clientSession{
		s:      server,
		tlscfg: tlscfg,
		muxcfg: server.cfg.NewMuxConfig(false),
		config: config,
	}
	c.apiClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return c.dialMux(ctx)
			},
			DisableKeepAlives: true,
		},
		Timeout: server.cfg.Timeout(),
	}
	return c
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
