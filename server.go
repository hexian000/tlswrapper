package main

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const network = "tcp"

var verbose = false

const (
	redialDelay       = 5 * time.Second
	checkIdleInterval = 10 * time.Second
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
			log.Println("stream error:", err)
		}
		return
	}
	if verbose {
		log.Println("stream close:", src.RemoteAddr(), "-x>", dst.RemoteAddr())
	}
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
			log.Println(err)
			continue
		}
		if verbose {
			log.Println("new session:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		}
		s.wg.Add(1)
		go s.serveMux(session, config)
	}
}

func (s *Server) dialTCP(from net.Addr, conn net.Conn, config *ServerConfig) {
	defer s.wg.Done()
	timeout := time.Duration(s.ConnectTimeout) * time.Second
	dial, err := net.DialTimeout(network, config.Forward, timeout)
	if err != nil {
		log.Println("dial TCP:", err)
		_ = conn.Close()
		return
	}
	if verbose {
		log.Println("stream open:", from, "->", dial.RemoteAddr())
	}
	s.wg.Add(1)
	go s.connCopy(conn, dial)
	s.wg.Add(1)
	go s.connCopy(dial, conn)
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

func (s *Server) dialTLS(addr string) (*yamux.Session, error) {
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
	log.Println("new session:", dial.LocalAddr(), "<->", dial.RemoteAddr(), "rtt:", rtt)
	s.wg.Add(1)
	go s.checkIdle(session)
	return session, nil
}

func (s *Server) dialMux(session *yamux.Session, conn net.Conn, config *ClientConfig) {
	defer s.wg.Done()
	dial, err := session.Open()
	if err != nil {
		log.Println("dial mux:", err)
		_ = session.Close()
		_ = conn.Close()
		return
	}
	if verbose {
		log.Println("stream open:", conn.LocalAddr(), "->", session.RemoteAddr())
	}
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
				log.Println(err)
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
	idleTicker := time.NewTicker(checkIdleInterval)
	defer idleTicker.Stop()
	pingTicker := time.NewTicker(time.Duration(s.KeepAlive) * time.Second)
	defer pingTicker.Stop()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.sessions, session)
	}()

	lastTick := time.Now()
	for {
		select {
		case <-idleTicker.C:
			now := time.Now()
			if now.Sub(lastTick) > timeout {
				log.Println("system hang detected, tick time:", now.Sub(lastTick))
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
			if numStreams > 0 || now.Sub(lastSeen) <= timeout {
				continue
			}
			log.Println("idle timeout expired:", session.LocalAddr(), "<x>", session.RemoteAddr())
			_ = session.Close()
			return
		case <-pingTicker.C:
			_, err := session.Ping()
			if err != nil {
				if err != yamux.ErrSessionShutdown {
					log.Println("keepalive error:", err)
				}
				_ = session.Close()
				return
			}
		case <-session.CloseChan():
			log.Println("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
			return
		}
	}
}

// Start the service
func (s *Server) Start() error {
	for _, server := range s.Server {
		addr := server.Listen
		if s.listeners[addr] != nil {
			continue
		}
		listener, err := net.Listen(network, addr)
		if err != nil {
			log.Fatalln(err)
		}
		s.listeners[addr] = listener
		log.Println("TLS listen:", listener.Addr())
		s.wg.Add(1)
		go s.serveTLS(listener, &server)
	}
	for _, client := range s.Client {
		addr := client.Listen
		if s.listeners[addr] != nil {
			continue
		}
		listener, err := net.Listen(network, addr)
		if err != nil {
			log.Fatalln(err)
		}
		s.listeners[addr] = listener
		log.Println("TCP listen:", listener.Addr())
		s.wg.Add(1)
		go s.serveTCP(listener, &client)
	}
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	close(s.shutdownCh)
	for addr, listener := range s.listeners {
		log.Println("listener close:", addr)
		_ = listener.Close()
	}
	s.listeners = make(map[string]net.Listener)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for session := range s.sessions {
			_ = session.Close()
		}
	}()
	s.wg.Wait()
	return nil
}

func (s *Server) closeChangedListener(cfg *Config) {
	if s.Config == nil {
		return
	}
	for _, server := range s.Server {
		found := false
		for _, newServer := range cfg.Server {
			if reflect.DeepEqual(server, newServer) {
				found = true
				break
			}
		}
		if !found {
			addr := server.Listen
			log.Println("listener close:", addr)
			_ = s.listeners[addr].Close()
			delete(s.listeners, addr)
		}
	}
	for _, client := range s.Client {
		found := false
		for _, newClient := range cfg.Client {
			if reflect.DeepEqual(client, newClient) {
				found = true
				break
			}
		}
		if !found {
			addr := client.Listen
			log.Println("listener close:", addr)
			_ = s.listeners[addr].Close()
			delete(s.listeners, addr)
		}
	}
}

// Load or reload configuration
func (s *Server) LoadConfig(cfg *Config) error {
	tlscfg := cfg.NewTLSConfig()
	if tlscfg == nil {
		return errors.New("TLS config error")
	}
	muxcfg := cfg.NewMuxConfig()
	if muxcfg == nil {
		return errors.New("mux config error")
	}
	s.closeChangedListener(cfg)
	s.Config = cfg
	s.tlscfg = cfg.NewTLSConfig()
	s.muxcfg = cfg.NewMuxConfig()
	return nil
}
