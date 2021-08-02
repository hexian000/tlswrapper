package main

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const network = "tcp"

// Server object
type Server struct {
	mu sync.Mutex
	*Config

	tlscfg *tls.Config
	muxcfg *yamux.Config

	lastSeen time.Time
}

// NewServer creates a server object
func NewServer(cfg *Config) *Server {
	return &Server{
		mu:     sync.Mutex{},
		Config: cfg,
		tlscfg: cfg.NewTLSConfig(),
		muxcfg: cfg.NewMuxConfig(),
	}
}

func (s *Server) connCopy(dst net.Conn, src net.Conn) {
	defer func() {
		_ = src.Close()
		_ = dst.Close()
		s.seen()
	}()
	_, err := io.Copy(dst, src)
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			log.Println("session error:", err)
		}
	} else {
		log.Println("session close:", src.RemoteAddr(), "-x>", dst.RemoteAddr())
	}
}

func (s *Server) serveTLS() {
	l, err := net.Listen(network, s.Config.TLSListen)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("TLS listen:", l.Addr())

	for {
		conn, err := l.Accept()
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
		log.Println("TLS connection:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		go s.serveMux(session)
		go s.checkIdle(session)
	}
}

func (s *Server) serveMux(session *yamux.Session) {
	for {
		conn, err := session.Accept()
		if err != nil {
			return
		}
		dial, err := net.Dial(network, s.Config.Dial)
		if err != nil {
			log.Println("serveMux dial:", err)
			_ = conn.Close()
			continue
		}
		log.Println("session open:", session.RemoteAddr(), "->", dial.RemoteAddr())
		go s.connCopy(conn, dial)
		go s.connCopy(dial, conn)
	}
}

func (s *Server) dialTLS() (session *yamux.Session, err error) {
	var dial net.Conn
	dial, err = net.Dial(network, s.TLSDial)
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
	log.Println("TLS connection:", dial.LocalAddr(), "<->", dial.RemoteAddr())
	go s.checkIdle(session)
	return
}

func (s *Server) serveTCP(session *yamux.Session) {
	l, err := net.Listen(network, s.Config.Listen)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("TCP listen:", l.Addr())

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		for session == nil || session.IsClosed() {
			session, err = s.dialTLS()
			if err != nil {
				log.Println(err)
				time.Sleep(10 * time.Second)
			}
		}
		dial, err := session.Open()
		if err != nil {
			log.Println("session open:", err)
			_ = session.Close()
			_ = conn.Close()
			continue
		}
		log.Println("session open:", conn.RemoteAddr(), "->", session.RemoteAddr())
		go s.connCopy(conn, dial)
		go s.connCopy(dial, conn)
	}
}

func (s *Server) seen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeen = time.Now()
}

func (s *Server) checkIdle(session *yamux.Session) {
	timeout := time.Duration(s.IdleTimeout) * time.Second
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		if session.NumStreams() != 0 {
			continue
		}
		lastSeen := func() time.Time {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.lastSeen
		}()
		if time.Since(lastSeen) <= timeout {
			continue
		}

		log.Println("TLS idle close:", session.LocalAddr(), "-x>", session.RemoteAddr())
		_ = session.Close()
		ticker.Stop()
		return
	}
}

// Start the service
func (s *Server) Start() error {
	if s.TLSListen != "" {
		go s.serveTLS()
	}
	if s.Listen != "" {
		go s.serveTCP(nil)
	}
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	// _ = s.l.Close()
	// log.Println("shutting down gracefully")
	return errors.New("graceful shutdown not implemented")
}
