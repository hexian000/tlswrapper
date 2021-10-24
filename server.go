package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"time"
	"tlswrapper/slog"

	"github.com/hashicorp/yamux"
)

const network = "tcp"

const redialDelay = 5 * time.Second

type sessionState struct {
	numStreams int
	lastSeen   time.Time
}

// Server object
type Server struct {
	mu sync.Mutex
	wg sync.WaitGroup
	*Config

	tlscfg *tls.Config
	muxcfg *yamux.Config

	listeners map[string]net.Listener
	sessions  map[*yamux.Session]sessionState
	contexts  map[context.Context]func()

	shutdownCh chan struct{}
}

// NewServer creates a server object
func NewServer() *Server {
	return &Server{
		listeners:  make(map[string]net.Listener),
		sessions:   make(map[*yamux.Session]sessionState),
		contexts:   make(map[context.Context]func()),
		shutdownCh: make(chan struct{}),
	}
}

func (s *Server) newContext(timeout time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts[ctx] = cancel
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

func (s *Server) connCopy(dst net.Conn, src net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = src.Close()
		_ = dst.Close()
	}()
	_, err := io.Copy(dst, src)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, yamux.ErrStreamClosed) {
		slog.Error("stream error:", err)
		return
	}
	slog.Verbose("stream close:", src.RemoteAddr(), "-x>", dst.RemoteAddr())
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
		slog.Verbose("new session:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sessions[session] = sessionState{0, time.Now()}
		}()
		s.wg.Add(1)
		go s.serveMux(session, config)
	}
}

func (s *Server) serveMux(session *yamux.Session, config *ServerConfig) {
	defer s.wg.Done()
	for {
		conn, err := session.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.dialTCP(session.RemoteAddr(), conn, config)
	}
}

func (s *Server) dialTCP(from net.Addr, conn net.Conn, config *ServerConfig) {
	defer s.wg.Done()
	slog.Verbose("dial TCP:", config.Forward)
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	defer s.deleteContext(ctx)
	var dailer net.Dialer
	dial, err := dailer.DialContext(ctx, network, config.Forward)
	if err != nil {
		slog.Error("dial TCP:", err)
		_ = conn.Close()
		return
	}
	slog.Verbose("stream open:", from, "->", dial.RemoteAddr())
	s.wg.Add(1)
	go s.connCopy(conn, dial)
	s.wg.Add(1)
	go s.connCopy(dial, conn)
}

func (s *Server) dialTLS(addr string) (*yamux.Session, error) {
	slog.Verbose("dial TLS:", addr)
	startTime := time.Now()
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	defer s.deleteContext(ctx)
	var dailer net.Dialer
	conn, err := dailer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s.SetConnParams(conn)
	tlsConn := tls.Client(conn, s.tlscfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	session, err := yamux.Client(tlsConn, s.muxcfg)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	slog.Info("new session:", conn.LocalAddr(), "<->", conn.RemoteAddr(), "setup:", time.Since(startTime))
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sessions[session] = sessionState{0, time.Now()}
	}()
	s.wg.Add(1)
	go s.checkIdle(session)
	return session, nil
}

func (s *Server) delay(time.Duration) bool {
	timer := time.NewTimer(redialDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.shutdownCh:
		return false
	}
	return true
}

func (s *Server) serveTCP(listener net.Listener, config *ClientConfig) {
	defer s.wg.Done()
	var session *yamux.Session = nil

	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		for session == nil || session.IsClosed() {
			session, err = s.dialTLS(config.Dial)
			if err != nil {
				slog.Warning(err)
				if !s.delay(redialDelay) {
					_ = conn.Close()
					return
				}
				continue
			}
		}
		dial, err := session.Open()
		if err != nil {
			slog.Error("dial mux:", err)
			_ = conn.Close()
			_ = session.Close()
			continue
		}
		slog.Verbose("stream open:", conn.LocalAddr(), "->", session.RemoteAddr())
		s.wg.Add(1)
		go s.connCopy(conn, dial)
		s.wg.Add(1)
		go s.connCopy(dial, conn)
	}
}

func (s *Server) checkIdle(session *yamux.Session) {
	defer s.wg.Done()
	if s.IdleTimeout <= 0 {
		return
	}
	timeout := time.Duration(s.IdleTimeout) * time.Second
	ticker := time.NewTicker(time.Duration(s.KeepAlive) * time.Second)
	defer ticker.Stop()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.sessions, session)
	}()

	lastTick := time.Now()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			if now.Sub(lastTick) > timeout {
				slog.Warning("system hang detected, tick time:", now.Sub(lastTick))
				_ = session.Close()
				return
			}
			lastTick = now
			numStreams := session.NumStreams()
			lastSeen := func(state sessionState) time.Time {
				s.mu.Lock()
				defer s.mu.Unlock()
				lastState, ok := s.sessions[session]
				if !ok || numStreams > 0 || lastState.numStreams > 0 {
					s.sessions[session] = state
					return state.lastSeen
				}
				return lastState.lastSeen
			}(sessionState{numStreams, now})
			if numStreams == 0 && now.Sub(lastSeen) > timeout {
				slog.Info("idle timeout expired:", session.LocalAddr(), "<x>", session.RemoteAddr())
				_ = session.Close()
				return
			}
			rtt, err := session.Ping()
			if err != nil {
				if errors.Is(err, yamux.ErrSessionShutdown) {
					slog.Info("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
				} else {
					slog.Error("keepalive:", session.LocalAddr(), "<x>", session.RemoteAddr(), "error:", err)
					_ = session.Close()
				}
				return
			}
			slog.Verbose("keepalive:", session.LocalAddr(), "<->", session.RemoteAddr(), "rtt:", rtt)
		case <-session.CloseChan():
			slog.Info("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
			return
		case <-s.shutdownCh:
			_ = session.Close()
			slog.Info("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
			return
		}
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
		listener, err := net.Listen(network, addr)
		if err != nil {
			return err
		}
		s.listeners[addr] = listener
		slog.Info("TCP listen:", listener.Addr())
		s.wg.Add(1)
		go s.serveTCP(listener, &s.Client[i])
	}
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
		n := len(s.contexts)
		if n > 0 {
			slog.Infof("cancelling %d operations", n)
			for _, cancel := range s.contexts {
				cancel()
			}
		}
		n = len(s.sessions)
		if n > 0 {
			slog.Infof("closing %d sessions", n)
			for session := range s.sessions {
				_ = session.Close()
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
	tlscfg, err := cfg.NewTLSConfig()
	if err != nil {
		return err
	}
	s.Config = cfg
	s.tlscfg = tlscfg
	s.muxcfg = cfg.NewMuxConfig()
	return nil
}
