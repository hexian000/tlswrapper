package main

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
)

// Protocol abstarcts a protocol
type Protocol interface {
	Accept(conn net.Conn)
}

// ServerProtocol is the server-side protocol
type ServerProtocol struct {
	*Server
}

// ClientProtocol is the client-side protocol
type ClientProtocol struct {
	*Server
}

// Accept run a server protocol
func (p *ServerProtocol) Accept(conn net.Conn) {
	p.SetConnParams(conn)
	conn = tls.Server(conn, p.tlscfg)
	dial, err := net.Dial(network, p.Dial)
	if err != nil {
		log.Println("session dial:", err)
		_ = conn.Close()
		return
	}
	log.Println("connection:", conn.RemoteAddr(), "<->", dial.RemoteAddr())
	go connCopy(conn, dial)
	go connCopy(dial, conn)
}

// Accept run a client protocol
func (p *ClientProtocol) Accept(conn net.Conn) {
	dial, err := net.Dial(network, p.Dial)
	if err != nil {
		log.Println("session dial:", err)
		_ = conn.Close()
		return
	}
	p.SetConnParams(dial)
	dial = tls.Client(dial, p.tlscfg)
	log.Println("connection:", conn.RemoteAddr(), "<->", dial.RemoteAddr())
	go connCopy(conn, dial)
	go connCopy(dial, conn)
}

func connCopy(dst net.Conn, src net.Conn) {
	defer func() {
		_ = src.Close()
		_ = dst.Close()
	}()
	_, err := io.Copy(dst, src)
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			log.Println(err)
		}
	} else {
		log.Println("connection:", src.RemoteAddr(), "-x>", dst.RemoteAddr())
	}
}
