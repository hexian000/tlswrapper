package session

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/hashicorp/yamux"
)

func ServeContext(ctx context.Context, conn net.Conn, cfg *Config) (*Session, error) {
	if tcpConn := conn.(*net.TCPConn); tcpConn != nil {
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
	}
	tlsConn := tls.Server(conn, cfg.TLS)
	err := tlsConn.HandshakeContext(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	mux, err := yamux.Server(tlsConn, cfg.Mux)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Session{
		mux:  mux,
		addr: conn.RemoteAddr(),
	}, nil
}
