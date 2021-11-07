package main

import (
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

func (s *Server) dialTLS(addr string, tlscfg *tls.Config) (*yamux.Session, error) {
	slog.Verbose("dial TLS:", addr)
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	if ctx == nil {
		return nil, errShutdown
	}
	defer s.deleteContext(ctx)

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
	s.wg.Add(1)
	go s.watchSession(sessionName, session)
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

func (c *clientSession) serveForward(listener net.Listener, config *ForwardConfig) {
	defer c.s.wg.Done()
	for {
		accepted, err := listener.Accept()
		if err != nil {
			return
		}
		dialed, err := c.dialMux()
		if err != nil {
			_ = accepted.Close()
			continue
		}
		dialed = proxy.Client(dialed, config.Forward)
		c.s.forward(accepted, dialed)
	}
}

func (c *clientSession) serveTCP(listener net.Listener) {
	defer c.s.wg.Done()
	for {
		accepted, err := listener.Accept()
		if err != nil {
			return
		}
		dialed, err := c.dialMux()
		if err != nil {
			_ = accepted.Close()
			continue
		}
		c.s.forward(accepted, dialed)
	}
}

func (c *clientSession) dialMux() (net.Conn, error) {
	select {
	case <-c.muxLock:
		defer func() {
			c.muxLock <- struct{}{}
		}()
	case <-c.s.shutdownCh:
		return nil, errShutdown
	}
	for c.mux == nil || c.mux.IsClosed() {
		var err error
		c.mux, err = c.s.dialTLS(c.config.Dial, c.s.tlscfg)
		if err == nil {
			break
		}
		if errors.Is(err, errShutdown) {
			return nil, errShutdown
		}
		slog.Warning("dial TLS:", err)
		select {
		case <-time.After(redialDelay):
		case <-c.s.shutdownCh:
			return nil, errShutdown
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
