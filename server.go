package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"runtime/debug"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/slog"
)

const network = "tcp"

var dialer net.Dialer

const (
	idleCheckInterval = 10 * time.Second
)

type sessionInfo struct {
	session  *yamux.Session
	created  time.Time
	lastSeen time.Time
	count    func() (r uint64, w uint64)
}

// Server object
type Server struct {
	mu sync.Mutex
	wg sync.WaitGroup
	*Config

	tlscfg *tls.Config
	muxcfg *yamux.Config

	dials     map[string]*clientSession
	listeners map[string]net.Listener
	sessions  map[string]sessionInfo
	contexts  map[context.Context]context.CancelFunc

	shutdownCh chan struct{}

	startTime time.Time
}

type handlerFunc func(context.Context, net.Conn)

// NewServer creates a server object
func NewServer() *Server {
	return &Server{
		listeners:  make(map[string]net.Listener),
		sessions:   make(map[string]sessionInfo),
		contexts:   make(map[context.Context]context.CancelFunc),
		shutdownCh: make(chan struct{}),
	}
}

func (s *Server) newContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.ConnectTimeout)*time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts[ctx] = cancel
	return ctx
}

func (s *Server) deleteContext(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.contexts[ctx]; ok {
		cancel()
		delete(s.contexts, ctx)
	}
}

func (s *Server) forward(accepted net.Conn, dialed net.Conn) {
	slog.Verbose("stream open:", accepted.LocalAddr(), "->", dialed.RemoteAddr())
	connCopy := func(dst net.Conn, src net.Conn) {
		defer s.wg.Done()
		defer func() {
			_ = src.Close()
			_ = dst.Close()
		}()
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic:", err)
			}
		}()
		_, err := io.Copy(dst, src)
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, yamux.ErrStreamClosed) {
			slog.Verbose("stream error:", err)
			return
		}
		slog.Verbose("stream close:", accepted.LocalAddr(), "-x>", dialed.RemoteAddr())
	}
	s.wg.Add(1)
	go connCopy(accepted, dialed)
	s.wg.Add(1)
	go connCopy(dialed, accepted)
}

type TLSHandler struct {
	server *Server
	config *ServerConfig
}

func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	start := time.Now()
	h.server.SetConnParams(conn)
	meteredConn := Meter(conn)
	tlsConn := tls.Server(meteredConn, h.server.tlscfg)
	err := tlsConn.HandshakeContext(ctx)
	if err != nil {
		slog.Error(err)
		return
	}
	session, err := yamux.Server(tlsConn, h.server.muxcfg)
	if err != nil {
		slog.Error(err)
		return
	}
	slog.Info("accept session:", conn.RemoteAddr(), "<->", conn.LocalAddr(), "setup:", time.Since(start))
	sessionName := fmt.Sprintf("%s <- %s", conn.LocalAddr(), conn.RemoteAddr())
	func() {
		h.server.mu.Lock()
		defer h.server.mu.Unlock()
		now := time.Now()
		h.server.sessions[sessionName] = sessionInfo{
			session:  session,
			created:  now,
			lastSeen: now,
			count:    meteredConn.Count,
		}
	}()
	if h.config.Forward == "" {
		go h.server.serveHTTP(session)
		return
	}
	go func() {
		_ = h.server.Serve(session, &DirectForwardHandler{
			h.server,
			h.config.Forward,
		})
	}()
}

func (s *Server) dialDirect(ctx context.Context, addr string) (net.Conn, error) {
	slog.Verbose("dial TCP:", addr)
	dialed, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return dialed, nil
}

func (s *Server) forwardDirect(ctx context.Context, accepted net.Conn, address string) {
	defer s.wg.Done()
	dialed, err := s.dialDirect(ctx, address)
	if err != nil {
		_ = accepted.Close()
		slog.Error("dial TCP:", err)
		return
	}
	s.forward(accepted, dialed)
}

type DirectForwardHandler struct {
	*Server
	forward string
}

func (h *DirectForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	h.forwardDirect(ctx, accepted, h.forward)
}

func (s *Server) closeAllSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, item := range s.sessions {
		_ = item.session.Close()
		delete(s.sessions, name)
	}
}

func (s *Server) checkIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	timeout := time.Duration(s.IdleTimeout) * time.Second
	for name, item := range s.sessions {
		session := item.session
		if session.IsClosed() {
			delete(s.sessions, name)
			continue
		}
		numStreams := session.NumStreams()
		if numStreams > 0 {
			item.lastSeen = time.Now()
			continue
		}
		if time.Since(item.lastSeen) > timeout {
			slog.Info("idle timeout expired:", session.LocalAddr(), "<x>", session.RemoteAddr())
			_ = session.Close()
		}
	}
}

func (s *Server) watchdog() {
	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()
	lastTick := time.Now()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			if now.Sub(lastTick) > 2*idleCheckInterval {
				slog.Warning("system hang detected, tick time:", now.Sub(lastTick))
				s.closeAllSessions()
				return
			}
			lastTick = now
			if s.IdleTimeout > 0 {
				s.checkIdle()
			}
		case <-s.shutdownCh:
			return
		}
	}
}

type Handler interface {
	Serve(context.Context, net.Conn)
}

func (s *Server) serveOne(accepted net.Conn, handler Handler) {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic:", r, string(debug.Stack()))
		}
	}()
	ctx := s.newContext()
	defer s.deleteContext(ctx)
	handler.Serve(ctx, accepted)
}

func (s *Server) Serve(listener net.Listener, handler Handler) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if err, ok := err.(net.Error); ok && err.Temporary() {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return err
		}
		s.wg.Add(1)
		go s.serveOne(conn, handler)
	}
}

func (s *Server) ListenAndServe(addr string, handler Handler) error {
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Error("listen", addr, ":", err)
		return err
	}
	slog.Info("listen:", listener.Addr())
	s.listeners[addr] = listener
	return s.Serve(listener, handler)
}

// Start the service
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, server := range s.Server {
		addr := server.Listen
		s.wg.Add(1)
		go func(config *ServerConfig) {
			defer s.wg.Done()
			_ = s.ListenAndServe(addr, &TLSHandler{s, config})
		}(&s.Server[i])
	}
	for i, client := range s.Client {
		tlscfg, err := s.NewTLSConfig(client.ServerName)
		if err != nil {
			return err
		}
		c := newClientSession(s, tlscfg, &s.Client[i])
		if client.HostName != "" {
			s.dials[client.HostName] = c
		}
		if addr := client.Listen; addr != "" && s.listeners[addr] == nil {
			s.wg.Add(1)
			go func(config *ClientConfig) {
				defer s.wg.Done()
				_ = s.ListenAndServe(addr, &ClientForwardHandler{c})
			}(&s.Client[i])
		}
		for j, forward := range client.ProxyForwards {
			addr := forward.Listen
			s.wg.Add(1)
			go func(config *ForwardConfig) {
				defer s.wg.Done()
				_ = s.ListenAndServe(addr, &ClientProxyHandler{c, config})
			}(&s.Client[i].ProxyForwards[j])
		}
	}
	if addr := s.Proxy.Listen; addr != "" && s.listeners[addr] == nil {
		listener, err := net.Listen(network, addr)
		if err != nil {
			return err
		}
		s.listeners[addr] = listener
		slog.Info("Proxy listen:", listener.Addr())
		go s.serveHTTP(listener)
	}
	go s.watchdog()
	s.startTime = time.Now()
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	for addr, listener := range s.listeners {
		slog.Info("listener close:", listener.Addr())
		_ = listener.Close()
		delete(s.listeners, addr)
	}
	close(s.shutdownCh)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.contexts) > 0 {
			for _, cancel := range s.contexts {
				cancel()
			}
		}
	}()
	slog.Info("waiting for unfinished connections")
	s.wg.Wait()
	return nil
}

// Load or reload configuration
func (s *Server) LoadConfig(cfg *Config) error {
	if s.Config != nil {
		if !reflect.DeepEqual(s.Config.Server, cfg.Server) ||
			!reflect.DeepEqual(s.Config.Client, cfg.Client) {
			slog.Warning("listener config changes are ignored")
		}
	}
	tlscfg, err := cfg.NewTLSConfig("")
	if err != nil {
		return err
	}
	s.Config = cfg
	s.tlscfg = tlscfg
	s.muxcfg = cfg.NewMuxConfig()
	return nil
}
