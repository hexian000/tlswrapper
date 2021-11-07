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

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/slog"
)

const network = "tcp"

var dialer net.Dialer

const (
	redialDelay       = 5 * time.Second
	idleCheckInterval = 10 * time.Second
)

type sessionInfo struct {
	session  *yamux.Session
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

func (s *Server) forward(accepted net.Conn, dialed net.Conn) {
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
		meteredConn := Meter(conn)
		tlsConn := tls.Server(meteredConn, s.tlscfg)
		session, err := yamux.Server(tlsConn, s.muxcfg)
		if err != nil {
			slog.Error(err)
			continue
		}
		slog.Info("accept session:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		sessionName := fmt.Sprintf("accepted: %s -> %s", conn.RemoteAddr(), conn.LocalAddr())
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sessions[sessionName] = sessionInfo{
				session:  session,
				lastSeen: time.Now(),
				count:    meteredConn.Count,
			}
		}()
		if config.Forward == "" {
			s.wg.Add(1)
			go s.serveHTTP(session, config)
		} else {
			s.wg.Add(1)
			go s.serveMux(session, config)
		}
		s.wg.Add(1)
		go s.watchSession(sessionName, session)
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
		go s.forwardTCP(accepted, config.Forward)
	}
}

func (s *Server) forwardTCP(accepted net.Conn, address string) {
	defer s.wg.Done()
	dialed, err := s.dialTCP(address)
	if err != nil {
		_ = accepted.Close()
		slog.Error("dial TCP:", err)
		return
	}
	s.forward(accepted, dialed)
}

func (s *Server) dialTCP(addr string) (net.Conn, error) {
	slog.Verbose("dial TCP:", addr)
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	if ctx == nil {
		return nil, errors.New("server is shutting down")
	}
	defer s.deleteContext(ctx)
	dialed, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return dialed, nil
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
		tlscfg, err := s.NewTLSConfig(client.ServerName)
		if err != nil {
			return err
		}
		c := newClientSession(s, tlscfg, &s.Client[i])
		if addr := client.Listen; addr != "" && s.listeners[addr] == nil {
			listener, err := net.Listen(network, addr)
			if err != nil {
				return err
			}
			s.listeners[addr] = listener
			slog.Info("TCP listen:", listener.Addr())
			s.wg.Add(1)
			go c.serveTCP(listener)
		}
		for j, forward := range client.ProxyForwards {
			addr := forward.Listen
			if s.listeners[addr] != nil {
				continue
			}
			listener, err := net.Listen(network, addr)
			if err != nil {
				return err
			}
			s.listeners[addr] = listener
			slog.Info("  forward:", listener.Addr(), "->", forward.Forward)
			s.wg.Add(1)
			go c.serveForward(listener, &s.Client[i].ProxyForwards[j])
		}
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
