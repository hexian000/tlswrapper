package session

import (
	"net"

	"github.com/hashicorp/yamux"
)

const network = "tcp"

type Session struct {
	mux *yamux.Session
	addr net.Addr
}

func (ss *Session) Addr() net.Addr {
	return ss.addr
}

func (ss *Session) Accept() (net.Conn, error) {
	return ss.mux.Accept()
}

func (ss *Session) Open() (net.Conn, error) {
	return ss.mux.Open()
}

func (ss *Session) Close() error {
	return ss.mux.Close()
}
