package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"time"
	"tlswrapper/slog"

	"github.com/hashicorp/yamux"
)

const network = "tcp"

const (
	redialDelay       = 5 * time.Second
	idleCheckInterval = 10 * time.Second
)

type sessionInfo struct {
	session  *yamux.Session
	lastSeen time.Time
}

// Server object
type Server struct {
	mu sync.Mutex
	wg sync.WaitGroup
	*Config

	tlscfg *tls.Config
	muxcfg *yamux.Config

	listeners map[string]net.Listener
	sessions  map[string]sessionInfo
	contexts  map[context.Context]func()

	shutdownCh chan struct{}
}

// NewServer creates a server object
func NewServer() *Server {
	return &Server{
		listeners:  make(map[string]net.Listener),
		sessions:   make(map[string]sessionInfo),
		contexts:   make(map[context.Context]func()),
		shutdownCh: make(chan struct{}),
	}
}

func (s *Server) newContext(timeout time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	if !func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		select {
		case <-s.shutdownCh:
			return false
		default:
		}
		s.contexts[ctx] = cancel
		return true
	}() {
		cancel()
		return nil
	}
	return ctx
}

func (s *Server) deleteContext(ctx context.Context) {
	if cancel := func() func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		cancel := s.contexts[ctx]
		delete(s.contexts, ctx)
		return cancel
	}(); cancel != nil {
		cancel()
	}
}

func (s *Server) pipe(accepted net.Conn, dialed net.Conn) {
	slog.Verbose("stream open:", accepted.LocalAddr(), "->", dialed.RemoteAddr())
	connCopy := func(dst net.Conn, src net.Conn) {
		defer s.wg.Done()
		defer func() {
			_ = src.Close()
			_ = dst.Close()
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

func (s *Server) serveTLS(listener net.Listener, config *ServerConfig) {
	defer s.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.SetConnParams(conn)
		tlsConn := tls.Server(conn, s.tlscfg)
		session, err := yamux.Server(tlsConn, s.muxcfg)
		if err != nil {
			slog.Error(err)
			continue
		}
		slog.Info("accept session:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		s.wg.Add(1)
		go s.serveMux(session, config)
		s.wg.Add(1)
		go s.watchSession(fmt.Sprintf("%p", session), session)
	}
}

func (s *Server) serveMux(session *yamux.Session, config *ServerConfig) {
	defer s.wg.Done()
	for {
		accepted, err := session.Accept()
		if err != nil {
			_ = session.Close()
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			dialed, err := s.dialTCP(config.Forward)
			if err != nil {
				_ = accepted.Close()
				slog.Error("dial TCP:", err)
			}
			s.pipe(accepted, dialed)
		}()
	}
}

func (s *Server) dialTCP(addr string) (net.Conn, error) {
	slog.Verbose("dial TCP:", addr)
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	if ctx == nil {
		return nil, errors.New("server is shutting down")
	}
	defer s.deleteContext(ctx)
	var dailer net.Dialer
	dialed, err := dailer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return dialed, nil
}

func (s *Server) dialTLS(ctx context.Context, addr string, tlscfg *tls.Config) (*yamux.Session, error) {
	slog.Verbose("dial TLS:", addr)
	startTime := time.Now()
	var dailer net.Dialer
	conn, err := dailer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s.SetConnParams(conn)
	tlsConn := tls.Client(conn, tlscfg)
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
	s.wg.Add(1)
	go s.watchSession(addr, session)
	return session, nil
}

func (s *Server) tryDialTLS(addr string, tlscfg *tls.Config) (*yamux.Session, bool) {
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	if ctx == nil {
		return nil, false
	}
	defer s.deleteContext(ctx)

	session, err := s.dialTLS(ctx, addr, tlscfg)
	if err == nil {
		return session, false
	}
	slog.Warning("dial TLS:", err)

	timer := time.NewTimer(redialDelay)
	defer timer.Stop()
	select {
	case <-s.shutdownCh:
		return nil, false
	case <-timer.C:
	}
	return nil, true
}

func (s *Server) serveTCP(listener net.Listener, tlscfg *tls.Config, config *ClientConfig) {
	defer s.wg.Done()
	var mux *yamux.Session = nil

	for {
		accepted, err := listener.Accept()
		if err != nil {
			return
		}
		for mux == nil || mux.IsClosed() {
			var retry bool
			mux, retry = s.tryDialTLS(config.Dial, tlscfg)
			if mux == nil && !retry {
				_ = accepted.Close()
				return
			}
		}
		dialed, err := mux.Open()
		if err != nil {
			slog.Error("dial mux:", err)
			_ = accepted.Close()
			_ = mux.Close()
			continue
		}
		s.pipe(accepted, dialed)
	}
}

func (s *Server) closeAllSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.sessions {
		_ = item.session.Close()
	}
}

func (s *Server) checkIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	timeout := time.Duration(s.IdleTimeout) * time.Second
	for _, item := range s.sessions {
		numStreams := item.session.NumStreams()
		if numStreams > 0 {
			item.lastSeen = time.Now()
			continue
		}
		if time.Since(item.lastSeen) > timeout {
			slog.Info("idle timeout expired:", item.session.LocalAddr(), "<x>", item.session.RemoteAddr())
			_ = item.session.Close()
		}
	}
}

func (s *Server) watchdog() {
	defer s.wg.Done()
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

func (s *Server) watchSession(name string, session *yamux.Session) {
	defer s.wg.Done()
	defer slog.Info("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sessions[name] = sessionInfo{session, time.Now()}
	}()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.sessions, name)
	}()

	select {
	case <-session.CloseChan():
	case <-s.shutdownCh:
		_ = session.Close()
	}
}

// Start the service
func (s *Server) Start() error {
	for i, server := range s.Server {
		addr := server.Listen
		if s.listeners[addr] != nil {
			continue
		}
		listener, err := net.Listen(network, addr)
		if err != nil {
			return err
		}
		s.listeners[addr] = listener
		slog.Info("TLS listen:", listener.Addr())
		s.wg.Add(1)
		go s.serveTLS(listener, &s.Server[i])
	}
	for i, client := range s.Client {
		addr := client.Listen
		if s.listeners[addr] != nil {
			continue
		}
		tlscfg, err := s.NewTLSConfig(client.ServerName)
		if err != nil {
			return err
		}
		listener, err := net.Listen(network, addr)
		if err != nil {
			return err
		}
		s.listeners[addr] = listener
		slog.Info("TCP listen:", listener.Addr())
		s.wg.Add(1)
		go s.serveTCP(listener, tlscfg, &s.Client[i])
	}
	s.wg.Add(1)
	go s.watchdog()
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	for _, listener := range s.listeners {
		slog.Info("listener close:", listener.Addr())
		_ = listener.Close()
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
