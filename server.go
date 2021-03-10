package main

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

const network = "tcp"

// Server object
type Server struct {
	*Config

	tlscfg *tls.Config
	muxcfg *yamux.Config
}

// NewServer creates a server object
func NewServer(cfg *Config) *Server {
	return &Server{
		Config: cfg,
		tlscfg: cfg.NewTLSConfig(),
		muxcfg: cfg.NewMuxConfig(),
	}
}

func connCopy(dst net.Conn, src net.Conn) {
	defer func() {
		_ = src.Close()
		_ = dst.Close()
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
	l, err := net.Listen(network, s.Config.Listen)
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
	}
}

func (s *Server) dialTLS() (session *yamux.Session, err error) {
	var dial net.Conn
	dial, err = net.Dial(network, s.Dial)
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
	return
}

func (s *Server) mustDialTLS() *yamux.Session {
	for {
		session, err := s.dialTLS()
		if err == nil {
			return session
		}
		log.Println(err)
		time.Sleep(10 * time.Second)
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
		log.Println("session open:", session.RemoteAddr(), "->", conn.RemoteAddr())
		go connCopy(conn, dial)
		go connCopy(dial, conn)
	}
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
		go connCopy(conn, dial)
		go connCopy(dial, conn)
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
	log.Fatalln("shutdown not implemented")
	// _ = s.l.Close()
	// log.Println("shutting down gracefully")
	return nil
}
