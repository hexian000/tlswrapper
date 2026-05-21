// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"context"
	"fmt"
	"net"

	"github.com/quic-go/quic-go"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// Dial establishes a new QUIC connection to addr and performs the h3mux
// client-side handshake.  The returned Session is ready to use.
//
// addr must be a host:port string resolvable as a UDP address.
// cfg.TLSConfig must not be nil; the h3mux ALPN is added automatically.
func Dial(ctx context.Context, addr string, cfg *Config) (mux.Session, error) {
	conn, err := quic.DialAddr(ctx, addr, cfg.tlsClientConfig(), cfg.quicConfig())
	if err != nil {
		return nil, fmt.Errorf("h3mux dial %s: %w", addr, err)
	}
	return clientHandshake(ctx, conn, cfg)
}

// NewSession wraps an already-established QUIC connection (server side) and
// performs the h3mux server-side handshake.  The returned Session is ready to use.
//
// conn is typically obtained from quic.Listener.Accept.
func NewSession(ctx context.Context, conn quic.Connection, cfg *Config) (mux.Session, error) {
	return serverHandshake(ctx, conn, cfg)
}

// clientHandshake opens the control stream, runs the client handshake, and
// returns an h3Session.
func clientHandshake(ctx context.Context, conn quic.Connection, cfg *Config) (mux.Session, error) {
	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, fmt.Errorf("%w: open control stream: %v", ErrHandshakeFailed, err)
	}
	peerID, peerRejectsOpen, err := doClientHandshake(ctrl, cfg.LocalID, cfg.RejectInbound)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, err
	}
	return newH3Session(conn, ctrl, peerID, peerRejectsOpen, cfg), nil
}

// serverHandshake accepts the control stream, runs the server handshake, and
// returns an h3Session.
func serverHandshake(ctx context.Context, conn quic.Connection, cfg *Config) (mux.Session, error) {
	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, fmt.Errorf("%w: accept control stream: %v", ErrHandshakeFailed, err)
	}
	peerID, peerRejectsOpen, err := doServerHandshake(ctrl, cfg.LocalID, cfg.RejectInbound)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, err
	}
	return newH3Session(conn, ctrl, peerID, peerRejectsOpen, cfg), nil
}

// Listen creates a QUIC listener on addr using the h3mux server TLS config
// (with ALPN injected) and the QUIC config derived from cfg.
// The caller is responsible for accepting connections and passing them to
// NewSession.
func Listen(addr string, cfg *Config) (*quic.Listener, error) {
	return quic.ListenAddr(addr, cfg.tlsServerConfig(), cfg.quicConfig())
}

// H3Mux wraps a Config and exposes Dial / NewSession as methods.
// Useful when you want to keep the Config alongside the mux factory.
type H3Mux struct {
	cfg *Config
}

// New creates a new H3Mux with the given config.
func New(cfg *Config) *H3Mux {
	return &H3Mux{cfg: cfg}
}

// Dial is equivalent to the package-level Dial function.
func (h *H3Mux) Dial(ctx context.Context, addr string) (mux.Session, error) {
	return Dial(ctx, addr, h.cfg)
}

// NewSession is equivalent to the package-level NewSession function.
func (h *H3Mux) NewSession(ctx context.Context, conn quic.Connection) (mux.Session, error) {
	return NewSession(ctx, conn, h.cfg)
}

// H3Listener accepts inbound mux sessions over QUIC.
// It wraps a quic.Listener and upgrades each accepted QUIC connection with
// the h3mux server-side handshake.
type H3Listener struct {
	l   *quic.Listener
	cfg *Config
}

// NewListener wraps l as a mux.Listener that upgrades each accepted QUIC
// connection using the h3mux server-side handshake with cfg.
func NewListener(l *quic.Listener, cfg *Config) *H3Listener {
	return &H3Listener{l: l, cfg: cfg}
}

// ListenMux is a convenience wrapper that calls Listen and wraps the result
// with NewListener.
func ListenMux(addr string, cfg *Config) (*H3Listener, error) {
	l, err := Listen(addr, cfg)
	if err != nil {
		return nil, err
	}
	return NewListener(l, cfg), nil
}

// compile-time interface checks.
var (
	_ mux.Dialer   = (*H3Mux)(nil)
	_ mux.Listener = (*H3Listener)(nil)
)

// AcceptSession accepts one QUIC connection and runs the h3mux server-side
// handshake, returning the resulting Session.
func (l *H3Listener) AcceptSession(ctx context.Context) (mux.Session, error) {
	conn, err := l.l.Accept(ctx)
	if err != nil {
		return nil, err
	}
	return NewSession(ctx, conn, l.cfg)
}

// Addr returns the listener's local network address.
func (l *H3Listener) Addr() net.Addr { return l.l.Addr() }

// Close closes the underlying QUIC listener.
func (l *H3Listener) Close() error { return l.l.Close() }
