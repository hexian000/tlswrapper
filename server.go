package main

import (
	"log"
	"net"
)

const network = "tcp"

// Server object
type Server struct {
	*Config
	Protocol

	l net.Listener
}

func (s *Server) serve() {
	for {
		conn, err := s.l.Accept()
		if err != nil {
			return
		}
		go s.Accept(conn)
	}
}

// Start the service
func (s *Server) Start() (err error) {
	log.Println("starting in mode:", s.Mode)
	s.l, err = net.Listen(network, s.Config.Listen)
	if err != nil {
		return
	}
	log.Println("forward:", s.Config.Listen, "->", s.Config.Dial)
	go s.serve()
	return
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	_ = s.l.Close()
	log.Println("shutting down gracefully")
	return nil
}
