package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/proxy"
	"github.com/hexian000/tlswrapper/slog"
)

var errShutdown = errors.New("server is shutting down")

func (s *Server) dialTLS(ctx context.Context, addr string, tlscfg *tls.Config) (*yamux.Session, error) {
	slog.Verbose("dial TLS:", addr)
	startTime := time.Now()
	var dailer net.Dialer
	conn, err := dailer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s.SetConnParams(conn)
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
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now()
		s.sessions[sessionName] = sessionInfo{
			session:  session,
			created:  now,
			lastSeen: now,
			count:    meteredConn.Count,
		}
	}()
	return session, nil
}

type clientSession struct {
	s       *Server
	config  *ClientConfig
	tlscfg  *tls.Config
	mux     *yamux.Session
	muxLock chan struct{}
}

func newClientSession(server *Server, tlscfg *tls.Config, config *ClientConfig) *clientSession {
	muxLock := make(chan struct{}, 1)
	muxLock <- struct{}{}
	return &clientSession{
		s:       server,
		tlscfg:  tlscfg,
		config:  config,
		muxLock: muxLock,
	}
}

func (c *clientSession) proxyDial(ctx context.Context, addr string) (net.Conn, error) {
	dialed, err := c.dialMux(ctx)
	if err != nil {
		return nil, err
	}
	conn := proxy.Client(dialed, addr)
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *clientSession) dialMux(ctx context.Context) (net.Conn, error) {
	select {
	case <-c.muxLock:
		defer func() {
			c.muxLock <- struct{}{}
		}()
	case <-c.s.shutdownCh:
		return nil, errShutdown
	}
	if c.mux == nil || c.mux.IsClosed() {
		var err error
		c.mux, err = c.s.dialTLS(ctx, c.config.Dial, c.s.tlscfg)
		if err != nil {
			slog.Warning("dial TLS:", err)
			return nil, err
		}
	}
	dialed, err := c.mux.Open()
	if err != nil {
		slog.Error("dial mux:", err)
		_ = c.mux.Close()
		return nil, err
	}
	return dialed, nil
}

type ClientProxyHandler struct {
	*clientSession
	config *ForwardConfig
}

func (c *ClientProxyHandler) Serve(ctx context.Context, accepted net.Conn) {
	defer c.s.wg.Done()
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
	defer h.s.wg.Done()
	dialed, err := h.dialMux(ctx)
	if err != nil {
		_ = accepted.Close()
		return
	}
	h.s.forward(accepted, dialed)
}
