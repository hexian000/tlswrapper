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

const (
	redialDelay       = 5 * time.Second
	idleCheckInterval = 10 * time.Second
)

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
			slog.Error("stream error:", err)
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
		slog.Verbose("accept session:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sessions[session] = sessionState{0, time.Now()}
		}()
		s.wg.Add(1)
		go s.serveMux(session, config)
		s.wg.Add(1)
		go s.watchSession(session)
	}
}

func (s *Server) serveMux(session *yamux.Session, config *ServerConfig) {
	defer s.wg.Done()
	for {
		accepted, err := session.Accept()
		if err != nil {
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

func (s *Server) dialTLS(addr string, ctx context.Context) (*yamux.Session, error) {
	slog.Verbose("dial TLS:", addr)
	startTime := time.Now()
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
	slog.Info("dial session:", conn.LocalAddr(), "<->", conn.RemoteAddr(), "setup:", time.Since(startTime))
	s.wg.Add(1)
	go s.watchSession(session)
	return session, nil
}

func (s *Server) tryDialTLS(addr string) (*yamux.Session, bool) {
	ctx := s.newContext(time.Duration(s.ConnectTimeout) * time.Second)
	if ctx == nil {
		return nil, false
	}
	defer s.deleteContext(ctx)

	session, err := s.dialTLS(addr, ctx)
	if err == nil {
		return session, false
	}
	slog.Warning(err)

	timer := time.NewTimer(redialDelay)
	defer timer.Stop()
	select {
	case <-s.shutdownCh:
		return nil, false
	case <-timer.C:
	}
	return nil, true
}

func (s *Server) serveTCP(listener net.Listener, config *ClientConfig) {
	defer s.wg.Done()
	var mux *yamux.Session = nil

	for {
		accepted, err := listener.Accept()
		if err != nil {
			return
		}
		for mux == nil || mux.IsClosed() {
			var retry bool
			mux, retry = s.tryDialTLS(config.Dial)
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

func (s *Server) checkIdle(session *yamux.Session) {
	defer s.wg.Done()
	timeout := time.Duration(s.IdleTimeout) * time.Second
	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()

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
		case <-session.CloseChan():
			return
		}
	}
}

func (s *Server) watchSession(session *yamux.Session) {
	defer s.wg.Done()
	defer slog.Info("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sessions[session] = sessionState{0, time.Now()}
	}()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.sessions, session)
	}()
	if s.IdleTimeout > 0 {
		s.wg.Add(1)
		go s.checkIdle(session)
	}

	select {
	case <-session.CloseChan():
	case <-s.shutdownCh:
		_ = session.GoAway()
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
	tlscfg, err := cfg.NewTLSConfig()
	if err != nil {
		return err
	}
	s.Config = cfg
	s.tlscfg = tlscfg
	s.muxcfg = cfg.NewMuxConfig()
	return nil
}
