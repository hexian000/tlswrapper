package main

import (
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

	shutdownCh chan struct{}
}

// NewServer creates a server object
func NewServer() *Server {
	return &Server{
		listeners:  make(map[string]net.Listener),
		sessions:   make(map[*yamux.Session]sessionState),
		shutdownCh: make(chan struct{}),
	}
}

func (s *Server) connCopy(dst net.Conn, src net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = src.Close()
		_ = dst.Close()
	}()
	_, err := io.Copy(dst, src)
	if err != nil {
		if !errors.Is(err, net.ErrClosed) && !errors.Is(err, yamux.ErrStreamClosed) {
			slog.Error("stream error:", err)
		}
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
		conn = tls.Server(conn, s.tlscfg)
		session, err := yamux.Server(conn, s.muxcfg)
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
	timeout := time.Duration(s.ConnectTimeout) * time.Second
	dial, err := net.DialTimeout(network, config.Forward, timeout)
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
	timeout := time.Duration(s.ConnectTimeout) * time.Second
	dial, err := net.DialTimeout(network, addr, timeout)
	if err != nil {
		return nil, err
	}
	s.SetConnParams(dial)
	dial = tls.Client(dial, s.tlscfg)
	session, err := yamux.Client(dial, s.muxcfg)
	if err != nil {
		_ = dial.Close()
		return nil, err
	}
	rtt, err := session.Ping()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	slog.Info("new session:", dial.LocalAddr(), "<->", dial.RemoteAddr(), "rtt:", rtt)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sessions[session] = sessionState{0, time.Now()}
	}()
	s.wg.Add(1)
	go s.checkIdle(session)
	return session, nil
}

func (s *Server) dialMux(session *yamux.Session, conn net.Conn, config *ClientConfig) {
	defer s.wg.Done()
	dial, err := session.Open()
	if err != nil {
		slog.Error("dial mux:", err)
		_ = session.Close()
		_ = conn.Close()
		return
	}
	slog.Verbose("stream open:", conn.LocalAddr(), "->", session.RemoteAddr())
	s.wg.Add(1)
	go s.connCopy(conn, dial)
	s.wg.Add(1)
	go s.connCopy(dial, conn)
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
				timer := time.NewTimer(redialDelay)
				select {
				case <-timer.C:
					timer.Stop()
				case <-s.shutdownCh:
					timer.Stop()
					_ = conn.Close()
					return
				}
			}
		}
		s.wg.Add(1)
		go s.dialMux(session, conn, config)
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
				if err != yamux.ErrSessionShutdown {
					slog.Error("keepalive:", session.LocalAddr(), "<x>", session.RemoteAddr(), "error:", err)
					_ = session.Close()
				}
				return
			}
			slog.Verbose("keepalive:", session.LocalAddr(), "<->", session.RemoteAddr(), "rtt:", rtt)
		case <-session.CloseChan():
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
	for addr, listener := range s.listeners {
		slog.Info("listener close:", addr)
		_ = listener.Close()
	}
	s.listeners = nil
	close(s.shutdownCh)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		slog.Infof("closing %d sessions", len(s.sessions))
		for session := range s.sessions {
			_ = session.Close()
		}
		s.sessions = nil
	}()
	slog.Info("waiting for unfinished pipes")
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
	tlscfg := cfg.NewTLSConfig()
	if tlscfg == nil {
		return errors.New("TLS config error")
	}
	muxcfg := cfg.NewMuxConfig()
	if muxcfg == nil {
		return errors.New("mux config error")
	}
	s.Config = cfg
	s.tlscfg = cfg.NewTLSConfig()
	s.muxcfg = cfg.NewMuxConfig()
	return nil
}
