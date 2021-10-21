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
	idle     bool
	lastSeen time.Time
}

// Server object
type Server struct {
	mu sync.Mutex
	*Config

	tlscfg *tls.Config
	muxcfg *yamux.Config

	listeners map[string]net.Listener
	sessions  map[*yamux.Session]sessionState
}

// NewServer creates a server object
func NewServer() *Server {
	return &Server{
		listeners: make(map[string]net.Listener),
		sessions:  make(map[*yamux.Session]sessionState),
	}
}

func (s *Server) connCopy(dst net.Conn, src net.Conn) {
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

func (s *Server) serveTLS(listener net.Listener, forwardAddr string) {
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
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sessions[session] = sessionState{true, time.Now()}
			if verbose {
				log.Println("new incoming session:", conn.RemoteAddr(), "<->", conn.LocalAddr())
			}
		}()
		go s.serveMux(session, forwardAddr)
		go s.checkIdle(session)
	}
}

func (s *Server) serveMux(session *yamux.Session, forwardAddr string) {
	for {
		conn, err := session.Accept()
		if err != nil {
			return
		}
		dial, err := net.Dial(network, forwardAddr)
		if err != nil {
			log.Println("stream dial:", err)
			_ = conn.Close()
			continue
		}
		if verbose {
			log.Println("stream open:", session.RemoteAddr(), "->", dial.RemoteAddr())
		}
		go s.connCopy(conn, dial)
		go s.connCopy(dial, conn)
	}
}

func (s *Server) dialTLS(dialAddr string) (session *yamux.Session, err error) {
	var dial net.Conn
	dial, err = net.Dial(network, dialAddr)
	if err != nil {
		return
	}
	s.SetConnParams(dial)
	dial = tls.Client(dial, s.tlscfg)
	session, err = yamux.Client(dial, s.muxcfg)
	if err != nil {
		_ = dial.Close()
		return
	}
	log.Println("new session:", dial.LocalAddr(), "<->", dial.RemoteAddr())
	go s.checkIdle(session)
	return
}

func (s *Server) serveTCP(listener net.Listener, dialAddr string) {
	var session *yamux.Session = nil

	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		for session == nil || session.IsClosed() {
			session, err = s.dialTLS(dialAddr)
			if err != nil {
				log.Println(err)
				time.Sleep(redialDelay)
			}
		}
		dial, err := session.Open()
		if err != nil {
			log.Println("stream open:", err)
			_ = session.Close()
			_ = conn.Close()
			continue
		}
		if verbose {
			log.Println("stream open:", conn.LocalAddr(), "->", session.RemoteAddr())
		}
		go s.connCopy(conn, dial)
		go s.connCopy(dial, conn)
	}
}

func (s *Server) checkIdle(session *yamux.Session) {
	if s.IdleTimeout <= 0 {
		return
	}
	timeout := time.Duration(s.IdleTimeout) * time.Second
	ticker := time.NewTicker(checkIdleInterval)
	defer func() {
		ticker.Stop()
		_ = session.Close()
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.sessions, session)
			if verbose {
				log.Println("session close:", session.LocalAddr(), "<x>", session.RemoteAddr())
			}
		}()
	}()

	lastTick := time.Now()
	for range ticker.C {
		if session.IsClosed() {
			return
		}
		now := time.Now()
		if now.Sub(lastTick) > timeout {
			log.Println("system hang detected, tick time:", now.Sub(lastTick))
			return
		}
		lastTick = now
		numStreams := session.NumStreams()
		if numStreams > 0 {
			func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.sessions[session] = sessionState{false, now}
			}()
			continue
		}
		idleSince := now
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			state := s.sessions[session]
			if !state.idle {
				s.sessions[session] = sessionState{true, idleSince}
			} else {
				idleSince = state.lastSeen
			}
		}()
		if now.Sub(idleSince) <= timeout {
			continue
		}
		log.Println("idle timeout expired: ", session.RemoteAddr())
		return
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
		go s.serveTLS(listener, server.Forward)
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
		go s.serveTCP(listener, client.Dial)
	}
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
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
