package session

import (
	"io"
	"net"

	"github.com/xtaci/smux"
)

const network = "tcp"

type Session struct {
	mux  *smux.Session
	addr net.Addr
}

func (ss *Session) Addr() net.Addr {
	return ss.addr
}

func (ss *Session) Accept() (io.ReadWriteCloser, error) {
	return ss.mux.Accept()
}

func (ss *Session) Open() (io.ReadWriteCloser, error) {
	return ss.mux.Open()
}

func (ss *Session) Close() error {
	return ss.mux.Close()
}
