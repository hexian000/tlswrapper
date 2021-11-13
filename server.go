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

const (
	idleCheckInterval = 10 * time.Second
)

type Session struct {
	mux      *yamux.Session
	meter    *MeteredConn
	created  time.Time
	lastSeen time.Time
}

func (i *Session) seen() {
	i.lastSeen = time.Now()
}

// Server object
type Server struct {
	mu sync.Mutex
	wg sync.WaitGroup

	cfg    *Config
	tlscfg *tls.Config
	muxcfg *yamux.Config

	dials     map[string]*clientSession
	listeners map[string]net.Listener
	sessions  map[string]*Session
	contexts  map[context.Context]context.CancelFunc

	dialer     net.Dialer
	shutdownCh chan struct{}

	startTime time.Time
}

// NewServer creates a server object
func NewServer() *Server {
	return &Server{
		dials:      make(map[string]*clientSession),
		listeners:  make(map[string]net.Listener),
		sessions:   make(map[string]*Session),
		contexts:   make(map[context.Context]context.CancelFunc),
		shutdownCh: make(chan struct{}),
	}
}

func (s *Server) newContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout())
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

func (s *Server) newSession(name string, mux *yamux.Session, meter *MeteredConn) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	info := &Session{
		mux:      mux,
		meter:    meter,
		created:  now,
		lastSeen: now,
	}
	s.sessions[name] = info
	return info
}

func (s *Server) forward(accepted net.Conn, dialed net.Conn) {
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
	}
	s.wg.Add(2)
	go connCopy(accepted, dialed)
	go connCopy(dialed, accepted)
}

type TLSHandler struct {
	server *Server
	config *ServerConfig
}

func (h *TLSHandler) Serve(ctx context.Context, conn net.Conn) {
	start := time.Now()
	h.server.cfg.SetConnParams(conn)
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
	_ = h.server.newSession(sessionName, session, meteredConn)
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
	dialed, err := s.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return dialed, nil
}

type DirectForwardHandler struct {
	server  *Server
	forward string
}

func (h *DirectForwardHandler) Serve(ctx context.Context, accepted net.Conn) {
	dialed, err := h.server.dialDirect(ctx, h.forward)
	if err != nil {
		_ = accepted.Close()
		slog.Error("dial TCP:", err)
		return
	}
	h.server.forward(accepted, dialed)
}

func (s *Server) closeAllSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, item := range s.sessions {
		_ = item.mux.Close()
		delete(s.sessions, name)
	}
}

func (s *Server) checkIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	timeout := time.Duration(s.cfg.IdleTimeout) * time.Second
	for name, item := range s.sessions {
		mux := item.mux
		if mux.IsClosed() {
			delete(s.sessions, name)
			continue
		}
		numStreams := mux.NumStreams()
		if numStreams > 0 {
			item.seen()
			continue
		}
		if time.Since(item.lastSeen) > timeout {
			slog.Info("idle timeout expired:", mux.LocalAddr(), "<x>", mux.RemoteAddr())
			_ = mux.Close()
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
			if s.cfg.IdleTimeout > 0 {
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
	for i, server := range s.cfg.Server {
		addr := server.Listen
		go func(config *ServerConfig) {
			_ = s.ListenAndServe(addr, &TLSHandler{s, config})
		}(&s.cfg.Server[i])
	}
	for i, client := range s.cfg.Client {
		tlscfg, err := s.cfg.NewTLSConfig(client.ServerName)
		if err != nil {
			return err
		}
		c := newClientSession(s, tlscfg, &s.cfg.Client[i])
		if client.HostName != "" {
			s.dials[client.HostName] = c
		}
		if addr := client.Listen; addr != "" && s.listeners[addr] == nil {
			go func(config *ClientConfig) {
				_ = s.ListenAndServe(addr, &ClientForwardHandler{c})
			}(&s.cfg.Client[i])
		}
		for j, forward := range client.ProxyForwards {
			addr := forward.Listen
			go func(config *ForwardConfig) {
				_ = s.ListenAndServe(addr, &ClientProxyHandler{c, config})
			}(&s.cfg.Client[i].ProxyForwards[j])
		}
	}
	if addr := s.cfg.Proxy.Listen; addr != "" && s.listeners[addr] == nil {
		listener, err := net.Listen(network, addr)
		if err != nil {
			return err
		}
		s.listeners[addr] = listener
		slog.Info("proxy listen:", listener.Addr())
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
		for ctx, cancel := range s.contexts {
			cancel()
			delete(s.contexts, ctx)
		}
	}()
	s.closeAllSessions()
	slog.Info("waiting for unfinished connections")
	s.wg.Wait()
	return nil
}

// Load or reload configuration
func (s *Server) LoadConfig(cfg *Config) error {
	if s.cfg != nil {
		if !reflect.DeepEqual(s.cfg.Server, cfg.Server) ||
			!reflect.DeepEqual(s.cfg.Client, cfg.Client) {
			slog.Warning("listener config changes are ignored")
		}
	}
	tlscfg, err := cfg.NewTLSConfig("")
	if err != nil {
		return err
	}
	s.cfg = cfg
	s.tlscfg = tlscfg
	s.muxcfg = cfg.NewMuxConfig(true)
	return nil
}
