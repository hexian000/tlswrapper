// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/http2"
)

// Config holds options for creating an h2mux session.
// Zero values for numeric/duration fields use built-in defaults.
type Config struct {
	// LocalID is the local service identity sent in the handshake.
	LocalID string
	// TLSConfig, when non-nil, causes Client/Server to perform a TLS handshake
	// on the raw connection before starting HTTP/2. nil means plaintext.
	TLSConfig *tls.Config

	// Client-side transport tuning.
	KeepAlive    time.Duration // default 25s
	PingTimeout  time.Duration // default 15s
	WriteTimeout time.Duration // default 15s

	// Server-side listener tuning.
	MaxConcurrentStreams uint32        // default 256
	IdleTimeout          time.Duration // default 0 (no idle timeout)
}

func (c *Config) keepAlive() time.Duration {
	if c.KeepAlive > 0 {
		return c.KeepAlive
	}
	return 25 * time.Second
}

func (c *Config) pingTimeout() time.Duration {
	if c.PingTimeout > 0 {
		return c.PingTimeout
	}
	return 15 * time.Second
}

func (c *Config) writeTimeout() time.Duration {
	if c.WriteTimeout > 0 {
		return c.WriteTimeout
	}
	return 15 * time.Second
}

func (c *Config) maxConcurrentStreams() uint32 {
	if c.MaxConcurrentStreams > 0 {
		return c.MaxConcurrentStreams
	}
	return 256
}

func (c *Config) newH2Transport() *http2.Transport {
	return &http2.Transport{
		TLSClientConfig:  c.TLSConfig,
		ReadIdleTimeout:  c.keepAlive(),
		PingTimeout:      c.pingTimeout(),
		WriteByteTimeout: c.writeTimeout(),
		AllowHTTP:        c.TLSConfig == nil,
	}
}

func (c *Config) newH2Server() *http2.Server {
	return &http2.Server{
		MaxConcurrentStreams: c.maxConcurrentStreams(),
		IdleTimeout:          c.IdleTimeout,
	}
}

// ErrHandshakeFailed is returned by Server when the h2mux protocol handshake fails.
var ErrHandshakeFailed = errors.New("h2mux: handshake failed")

// Client performs the TLS handshake (if cfg.TLSConfig is non-nil) and the h2mux
// protocol handshake over conn, returning a client-mode Session on success.
// conn is expected to be a raw (e.g. TCP) connection; Client wraps it with TLS
// internally when needed.
func Client(ctx context.Context, conn net.Conn, cfg *Config) (*Session, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	scheme := "http"
	if cfg.TLSConfig != nil {
		tlsConn := tls.Client(conn, cfg.TLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
		scheme = "https"
	}
	_ = conn.SetDeadline(time.Time{})

	dialAddr := conn.RemoteAddr().String()
	tag := fmt.Sprintf("? => %v", dialAddr)
	if cfg.LocalID != "" {
		tag = fmt.Sprintf("%q => %v", cfg.LocalID, dialAddr)
	}

	wrappedConn, connCloseCh := notifyConnClose(conn)
	transport := cfg.newH2Transport()
	h2conn, err := transport.NewClientConn(wrappedConn)
	if err != nil {
		return nil, err
	}
	return newClientSession(ctx, h2conn, connCloseCh, dialAddr, scheme, cfg.LocalID, tag)
}

// Server performs the TLS handshake (if cfg.TLSConfig is non-nil) and waits for
// the h2mux protocol handshake from the client, returning a server-mode Session
// on success. conn is expected to be a raw (e.g. TCP) connection; Server wraps
// it with TLS internally when needed.
func Server(ctx context.Context, conn net.Conn, cfg *Config) (*Session, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	if cfg.TLSConfig != nil {
		tlsConn := tls.Server(conn, cfg.TLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	}
	_ = conn.SetDeadline(time.Time{})

	sess := newServerSession(conn.LocalAddr(), conn.RemoteAddr(), cfg.LocalID)
	sess.rawConn = conn
	h2srv := cfg.newH2Server()

	go func() {
		h2srv.ServeConn(conn, &http2.ServeConnOpts{Handler: sess})
		_ = sess.Close()
	}()

	select {
	case <-sess.ready:
		if !sess.helloOK {
			_ = sess.Close()
			return nil, ErrHandshakeFailed
		}
		return sess, nil
	case <-ctx.Done():
		_ = sess.Close()
		return nil, ctx.Err()
	case <-sess.closedCh:
		return nil, ErrHandshakeFailed
	}
}
