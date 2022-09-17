package session

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/hashicorp/yamux"
)

func DialContext(ctx context.Context, address string, cfg *Config) (*Session, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if tcpConn := conn.(*net.TCPConn); tcpConn != nil {
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
	}
	tlsConn := tls.Client(conn, cfg.TLS)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	mux, err := yamux.Client(tlsConn, cfg.Mux)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Session{
		mux:  mux,
		addr: conn.RemoteAddr(),
	}, nil
}
