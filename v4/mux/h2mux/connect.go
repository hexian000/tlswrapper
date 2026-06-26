// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	mux "github.com/hexian000/tlswrapper/v4/mux"
	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// compile-time check that H2Mux implements mux.Dialer.
var _ mux.Dialer = (*H2Mux)(nil)

// H2Mux implements mux.Dialer backed by gRPC over HTTP/2.
type H2Mux struct {
	cfg *Config
}

// New returns an H2Mux that creates sessions using cfg.
func New(cfg *Config) *H2Mux {
	return &H2Mux{cfg: cfg}
}

// Dial implements mux.Dialer by dialing addr over TCP and running the h2mux
// client-side handshake.
func (h *H2Mux) Dial(ctx context.Context, addr string) (mux.Session, error) {
	conn, err := h.cfg.Dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if h.cfg.ConnSetup != nil {
		h.cfg.ConnSetup(conn)
	}
	ss, err := Client(ctx, conn, h.cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ss, nil
}

// H2Listener accepts inbound mux sessions over TCP.
// It wraps a net.Listener and upgrades each accepted connection with the
// h2mux server-side handshake.
type H2Listener struct {
	l   net.Listener
	cfg *Config
}

// NewListener wraps l as a mux.Listener that upgrades each accepted TCP
// connection using the h2mux server-side handshake with cfg.
func NewListener(l net.Listener, cfg *Config) *H2Listener {
	return &H2Listener{l: l, cfg: cfg}
}

// compile-time check that H2Listener implements mux.Listener.
var _ mux.Listener = (*H2Listener)(nil)

// h2InboundSession holds an accepted TCP connection in a pre-handshake state.
// It implements mux.Session: Handshake(ctx) runs the h2mux server-side setup;
// all other stream-level methods call Handshake implicitly using
// context.Background() when called before an explicit Handshake, matching
// the crypto/tls.Conn behaviour.
type h2InboundSession struct {
	conn net.Conn
	cfg  *Config

	mu            sync.Mutex
	handshakeDone atomic.Bool // set true AFTER ss/handshakeErr are stored
	ss            mux.Session
	handshakeErr  error
}

// compile-time check that h2InboundSession implements mux.Session.
var _ mux.Session = (*h2InboundSession)(nil)

// doHandshake performs the h2mux server-side handshake exactly once.
// The mutex is held for the entire handshake duration, matching crypto/tls.Conn.
func (s *h2InboundSession) doHandshake(ctx context.Context) error {
	if s.handshakeDone.Load() {
		return s.handshakeErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handshakeDone.Load() {
		return s.handshakeErr
	}
	ss, err := Server(ctx, s.conn, s.cfg)
	if err != nil {
		_ = s.conn.Close()
		s.handshakeErr = err
		s.handshakeDone.Store(true)
		return err
	}
	s.ss = ss
	s.handshakeDone.Store(true)
	return nil
}

func (s *h2InboundSession) Handshake(ctx context.Context) error {
	return s.doHandshake(ctx)
}

// delegate returns the ready session, running an implicit handshake if needed.
func (s *h2InboundSession) delegate() (mux.Session, error) {
	if err := s.doHandshake(context.Background()); err != nil {
		return nil, err
	}
	return s.ss, nil // safe: ss is stored before handshakeDone is set
}

func (s *h2InboundSession) Open(ctx context.Context) (net.Conn, error) {
	ss, err := s.delegate()
	if err != nil {
		return nil, err
	}
	return ss.Open(ctx)
}

func (s *h2InboundSession) Accept() (net.Conn, error) {
	ss, err := s.delegate()
	if err != nil {
		return nil, err
	}
	return ss.Accept()
}

func (s *h2InboundSession) Close() error {
	if s.handshakeDone.Load() {
		if s.ss != nil {
			return s.ss.Close()
		}
		return nil // handshake already failed; conn was closed in doHandshake
	}
	return s.conn.Close()
}

func (s *h2InboundSession) IsClosed() bool {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.IsClosed()
	}
	return false
}

func (s *h2InboundSession) CloseChan() <-chan struct{} {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.CloseChan()
	}
	return nil
}

func (s *h2InboundSession) IdleChan() <-chan struct{} {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.IdleChan()
	}
	return nil
}

func (s *h2InboundSession) Stats() *mux.SessionMetrics {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.Stats()
	}
	return nil
}

func (s *h2InboundSession) PeerIdentity() string {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.PeerIdentity()
	}
	return ""
}

func (s *h2InboundSession) LocalAddr() net.Addr {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.LocalAddr()
	}
	return s.conn.LocalAddr()
}

func (s *h2InboundSession) RemoteAddr() net.Addr {
	if s.handshakeDone.Load() && s.ss != nil {
		return s.ss.RemoteAddr()
	}
	return s.conn.RemoteAddr()
}

// Accept accepts one TCP connection, applies optional socket setup, and
// returns a Session whose Handshake runs the h2mux server-side setup.
func (l *H2Listener) Accept() (mux.Session, error) {
	conn, err := l.l.Accept()
	if err != nil {
		return nil, err
	}
	if l.cfg.ConnSetup != nil {
		l.cfg.ConnSetup(conn)
	}
	return &h2InboundSession{conn: conn, cfg: l.cfg}, nil
}

// Addr returns the listener's local network address.
func (l *H2Listener) Addr() net.Addr { return l.l.Addr() }

// Close closes the underlying listener.
func (l *H2Listener) Close() error { return l.l.Close() }

// oneConnListener is a net.Listener that serves exactly one pre-established connection.
type oneConnListener struct {
	ch   chan net.Conn
	addr net.Addr
	once sync.Once
}

func newOneConnListener(conn net.Conn) *oneConnListener {
	ch := make(chan net.Conn, 1)
	ch <- conn
	return &oneConnListener{ch: ch, addr: conn.LocalAddr()}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	conn, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *oneConnListener) Close() error {
	l.once.Do(func() { close(l.ch) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return l.addr }

// Client performs the TLS handshake (if cfg.TLSConfig is non-nil) and the mux
// protocol handshake over conn, returning a client-mode Session on success.
func Client(ctx context.Context, conn net.Conn, cfg *Config) (mux.Session, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	tlscfg := cfg.appliedTLSConfig()
	if tlscfg != nil {
		conn = tls.Client(conn, tlscfg)
	}
	if cfg.WriteTimeout > 0 {
		conn = &writeTimeoutConn{Conn: conn, timeout: cfg.WriteTimeout}
	}

	sh := newMuxStatsHandler()
	opts := cfg.grpcDialOptions()
	opts = append(opts, grpc.WithStatsHandler(sh))
	connCh := make(chan net.Conn, 1)
	connCh <- conn
	opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		select {
		case c := <-connCh:
			return c, nil
		default:
			return nil, net.ErrClosed
		}
	}))

	cc, err := grpc.NewClient("passthrough:///mux", opts...)
	if err != nil {
		return nil, err
	}

	// sessionCtx (and the Control RPC) live for the entire session lifetime;
	// using ctx would cancel the stream when the caller's deadline expires.
	sessionCtx, cancel := context.WithCancel(context.Background())

	grpcClient := muxpb.NewMuxClient(cc)

	ctrlStream, err := grpcClient.Control(sessionCtx)
	if err != nil {
		cancel()
		_ = cc.Close()
		return nil, fmt.Errorf("mux: control: %w", err)
	}

	// Perform the client-side handshake in a goroutine so we can respect ctx's deadline.
	type hsResult struct {
		peerIdentity       string
		peerRejectsInbound bool
		err                error
	}
	hsCh := make(chan hsResult, 1)
	go func() {
		peerIdentity, peerRejectsInbound, err := doClientHandshake(ctrlStream, cfg.LocalID, cfg.RejectInbound)
		hsCh <- hsResult{peerIdentity, peerRejectsInbound, err}
	}()

	var peerIdentity string
	var peerRejectsInbound bool
	select {
	case res := <-hsCh:
		if res.err != nil {
			cancel()
			_ = cc.Close()
			return nil, fmt.Errorf("mux: handshake: %w", res.err)
		}
		peerIdentity = res.peerIdentity
		peerRejectsInbound = res.peerRejectsInbound
	case <-ctx.Done():
		cancel()
		_ = cc.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	cleanup := func() {
		cancel()
		_ = cc.Close()
	}

	return newClientSession(
		ctrlStream,
		grpcClient,
		sessionCtx,
		cancel,
		cleanup,
		conn.LocalAddr(), conn.RemoteAddr(),
		peerIdentity,
		peerRejectsInbound,
		&sh.metrics,
		sh.idleNotify,
	), nil
}

// muxServer is the gRPC server-side implementation for one mux session.
type muxServer struct {
	muxpb.UnimplementedMuxServer
	cfg        *Config
	localAddr  net.Addr
	remoteAddr net.Addr
	sh         *muxStatsHandler

	// ready delivers the serverSession to Server() after the handshake succeeds.
	ready chan *serverSession

	// sess is set by Control() after handshake; Stream() handlers wait on sessReady.
	mu          sync.RWMutex
	ctrlStarted bool // a Control RPC has been received; reject duplicates
	sess        *serverSession
	sessReady   chan struct{} // closed once sess is set
}

func newMuxServer(cfg *Config, localAddr, remoteAddr net.Addr, sh *muxStatsHandler) *muxServer {
	return &muxServer{
		cfg:        cfg,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		sh:         sh,
		ready:      make(chan *serverSession, 1),
		sessReady:  make(chan struct{}),
	}
}

// Control is the long-lived control stream handler. It performs the server-side
// handshake, creates the Session, and then stays open until the session closes.
// Only one Control RPC is allowed per connection; duplicates are rejected
// (a second close of sessReady would panic and kill the process).
func (svc *muxServer) Control(stream muxpb.Mux_ControlServer) error {
	svc.mu.Lock()
	if svc.ctrlStarted {
		svc.mu.Unlock()
		return errDuplicateControl
	}
	svc.ctrlStarted = true
	svc.mu.Unlock()
	peerIdentity, peerRejectsInbound, err := doServerHandshake(stream, svc.cfg.LocalID, svc.cfg.RejectInbound)
	if err != nil {
		return err
	}

	sess := newServerSession(
		stream,
		nil, // cleanup wired after grpcSrv reference is available in Server()
		svc.localAddr, svc.remoteAddr,
		peerIdentity,
		peerRejectsInbound,
		&svc.sh.metrics,
		svc.sh.idleNotify,
	)

	svc.mu.Lock()
	svc.sess = sess
	close(svc.sessReady)
	svc.mu.Unlock()

	select {
	case svc.ready <- sess:
	case <-stream.Context().Done():
		_ = sess.Close()
		return stream.Context().Err()
	}

	// Hold the RPC open until the session closes.
	<-sess.CloseChan()
	return nil
}

// Stream routes one Stream RPC into the session after Control succeeds.
func (svc *muxServer) Stream(stream muxpb.Mux_StreamServer) error {
	// Wait for the Control handshake to complete before accepting streams.
	select {
	case <-svc.sessReady:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}

	svc.mu.RLock()
	sess := svc.sess
	svc.mu.RUnlock()

	var requestID string
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if vals := md.Get(metaRequestIDKey); len(vals) > 0 {
			requestID = vals[0]
		}
	}

	conn := newServerSideStream(stream, svc.localAddr, svc.remoteAddr, nil, func() {
		_ = sess.Close()
	})
	sess.DeliverStream(requestID, conn)

	// Keep the handler alive until Close() is called on conn (or context is done).
	select {
	case <-conn.doneCh:
	case <-stream.Context().Done():
	}
	return nil
}

// Server performs the TLS handshake (if cfg.TLSConfig is non-nil) and waits for
// the mux protocol handshake from the client, returning a server-mode Session
// on success.
func Server(ctx context.Context, conn net.Conn, cfg *Config) (mux.Session, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	tlscfg := cfg.appliedTLSConfig()
	if tlscfg != nil {
		conn = tls.Server(conn, tlscfg)
	}
	if cfg.WriteTimeout > 0 {
		conn = &writeTimeoutConn{Conn: conn, timeout: cfg.WriteTimeout}
	}

	sh := newMuxStatsHandler()
	svc := newMuxServer(cfg, conn.LocalAddr(), conn.RemoteAddr(), sh)
	grpcSrv := grpc.NewServer(append(cfg.grpcServerOptions(), grpc.StatsHandler(sh))...)
	muxpb.RegisterMuxServer(grpcSrv, svc)

	listener := newOneConnListener(conn)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = grpcSrv.Serve(listener)
	}()

	select {
	case sess := <-svc.ready:
		// Wire up cleanup: stopping the gRPC server closes all streams.
		sess.cleanup = func() { grpcSrv.Stop() }
		_ = conn.SetDeadline(time.Time{})
		return sess, nil
	case <-ctx.Done():
		grpcSrv.Stop()
		select {
		case sess := <-svc.ready:
			_ = sess.Close()
		default:
		}
		return nil, ctx.Err()
	case <-serveDone:
		return nil, ErrHandshakeFailed
	}
}
