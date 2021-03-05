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

func (s *Server) runServer() {
	l, err := net.Listen(network, s.Config.Listen)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("forward:", l.Addr(), "->", s.Config.Dial)

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		s.SetConnParams(conn)
		conn = tls.Server(conn, s.tlscfg)
		log.Println("connection:", conn.RemoteAddr(), "<->", conn.LocalAddr())
		go func(conn net.Conn) {
			session, err := yamux.Server(conn, s.muxcfg)
			if err != nil {
				log.Println(err)
				return
			}
			for {
				conn, err := session.Accept()
				if err != nil {
					return
				}
				log.Println("session open:", session.RemoteAddr(), "->", conn.RemoteAddr())
				dial, err := net.Dial(network, s.Config.Dial)
				if err != nil {
					_ = conn.Close()
					return
				}
				go connCopy(conn, dial)
				go connCopy(dial, conn)
			}
		}(conn)
	}
}

func (s *Server) muxDial() (session *yamux.Session, err error) {
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
	log.Println("connection:", dial.LocalAddr(), "<->", dial.RemoteAddr())
	return
}

func (s *Server) runClient() {
	l, err := net.Listen(network, s.Config.Listen)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("forward:", l.Addr(), "->", s.Config.Dial)
	session, err := s.muxDial()
	if err != nil {
		log.Fatalln(err)
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		s.SetConnParams(conn)

		for session == nil || session.IsClosed() {
			session, err = s.muxDial()
			if err != nil {
				log.Println(err)
				time.Sleep(10 * time.Second)
			}
		}
		go func(session *yamux.Session, conn net.Conn) {
			dial, err := session.Open()
			if err != nil {
				log.Println(err)
				_ = session.Close()
				_ = conn.Close()
				return
			}
			log.Println("session open:", conn.RemoteAddr(), "->", session.RemoteAddr())
			go connCopy(conn, dial)
			go connCopy(dial, conn)
		}(session, conn)
	}
}

// Start the service
func (s *Server) Start() error {
	log.Println("starting in mode:", s.Mode)
	if s.IsServer() {
		go s.runServer()
	} else {
		go s.runClient()
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
