// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

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
func NewSession(ctx context.Context, conn *quic.Conn, cfg *Config) (mux.Session, error) {
	return serverHandshake(ctx, conn, cfg)
}

// applyHandshakeDeadline bounds blocking reads/writes on the control stream
// by the context deadline, so a peer that opens the control stream but never
// completes the hello exchange cannot park the handshake goroutine forever.
// The deadline is cleared by the caller after a successful handshake.
func applyHandshakeDeadline(ctx context.Context, ctrl *quic.Stream) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = ctrl.SetDeadline(deadline)
	}
}

// clientHandshake opens the control stream, runs the client handshake, and
// returns an h3Session.
func clientHandshake(ctx context.Context, conn *quic.Conn, cfg *Config) (mux.Session, error) {
	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, fmt.Errorf("%w: open control stream: %v", ErrHandshakeFailed, err)
	}
	applyHandshakeDeadline(ctx, ctrl)
	peerID, peerRejectsOpen, err := doClientHandshake(ctrl, cfg.LocalID, cfg.RejectInbound)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, err
	}
	_ = ctrl.SetDeadline(time.Time{})
	return newH3Session(conn, ctrl, peerID, peerRejectsOpen, cfg), nil
}

// serverHandshake accepts the control stream, runs the server handshake, and
// returns an h3Session.
func serverHandshake(ctx context.Context, conn *quic.Conn, cfg *Config) (mux.Session, error) {
	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, fmt.Errorf("%w: accept control stream: %v", ErrHandshakeFailed, err)
	}
	applyHandshakeDeadline(ctx, ctrl)
	peerID, peerRejectsOpen, err := doServerHandshake(ctrl, cfg.LocalID, cfg.RejectInbound)
	if err != nil {
		_ = conn.CloseWithError(0, "handshake failed")
		return nil, err
	}
	_ = ctrl.SetDeadline(time.Time{})
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
func (h *H3Mux) NewSession(ctx context.Context, conn *quic.Conn) (mux.Session, error) {
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

// h3InboundSession holds an accepted QUIC connection (QUIC/TLS established)
// before the h3mux application-level handshake has been completed.
// It implements mux.Session: Handshake(ctx) runs the h3mux control-stream
// setup; all other stream-level methods call Handshake implicitly using
// context.Background() when called before an explicit Handshake, matching
// the crypto/tls.Conn behaviour.
type h3InboundSession struct {
	conn *quic.Conn
	cfg  *Config

	mu            sync.Mutex
	handshakeDone atomic.Bool // set true AFTER ss/handshakeErr are stored
	ss            mux.Session
	handshakeErr  error
}

// compile-time check that h3InboundSession implements mux.Session.
var _ mux.Session = (*h3InboundSession)(nil)

// doHandshake performs the h3mux server-side handshake exactly once.
// The mutex is held for the entire handshake duration, matching crypto/tls.Conn.
func (s *h3InboundSession) doHandshake(ctx context.Context) error {
	if s.handshakeDone.Load() {
		return s.handshakeErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handshakeDone.Load() {
		return s.handshakeErr
	}
	ss, err := serverHandshake(ctx, s.conn, s.cfg)
	if err != nil {
		s.handshakeErr = err
		s.handshakeDone.Store(true)
		return err
	}
	s.ss = ss
	s.handshakeDone.Store(true)
	return nil
}

func (s *h3InboundSession) Handshake(ctx context.Context) error {
	return s.doHandshake(ctx)
}

// delegate returns the ready session, running an implicit handshake if needed.
func (s *h3InboundSession) delegate() (mux.Session, error) {
	if err := s.doHandshake(context.Background()); err != nil {
		return nil, err
	}
	return s.ss, nil // safe: ss is stored before handshakeDone is set
}

func (s *h3InboundSession) Open(ctx context.Context) (net.Conn, error) {
	ss, err := s.delegate()
	if err != nil {
		return nil, err
	}
	return ss.Open(ctx)
}

func (s *h3InboundSession) Accept() (net.Conn, error) {
	ss, err := s.delegate()
	if err != nil {
		return nil, err
	}
	return ss.Accept()
}

func (s *h3InboundSession) Close() error {
	if s.handshakeDone.Load() {
		if s.ss != nil {
			return s.ss.Close()
		}
		return nil // handshake already failed; conn was closed in serverHandshake
	}
	return s.conn.CloseWithError(0, "handshake cancelled")
}

func (s *h3InboundSession) IsClosed() bool {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.IsClosed()
	}
	return false
}

func (s *h3InboundSession) CloseChan() <-chan struct{} {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.CloseChan()
	}
	return nil
}

func (s *h3InboundSession) IdleChan() <-chan struct{} {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.IdleChan()
	}
	return nil
}

func (s *h3InboundSession) Stats() *mux.SessionMetrics {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.Stats()
	}
	return nil
}

func (s *h3InboundSession) PeerIdentity() string {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.PeerIdentity()
	}
	return ""
}

func (s *h3InboundSession) LocalAddr() net.Addr {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.LocalAddr()
	}
	return s.conn.LocalAddr()
}

func (s *h3InboundSession) RemoteAddr() net.Addr {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.RemoteAddr()
	}
	return s.conn.RemoteAddr()
}

// Accept waits for a new QUIC connection (including QUIC/TLS negotiation) and
// returns a Session whose Handshake performs the h3mux control-stream setup.
// Accept blocks until a connection arrives or the listener is closed.
func (l *H3Listener) Accept() (mux.Session, error) {
	conn, err := l.l.Accept(context.Background())
	if err != nil {
		return nil, err
	}
	return &h3InboundSession{conn: conn, cfg: l.cfg}, nil
}

// Addr returns the listener's local network address.
func (l *H3Listener) Addr() net.Addr { return l.l.Addr() }

// Close closes the underlying QUIC listener.
func (l *H3Listener) Close() error { return l.l.Close() }
