// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
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

// Client implements mux.Dialer.
func (h *H2Mux) Client(ctx context.Context, conn net.Conn) (mux.Session, error) {
	return Client(ctx, conn, h.cfg)
}

// Server implements mux.Dialer.
func (h *H2Mux) Server(ctx context.Context, conn net.Conn) (mux.Session, error) {
	return Server(ctx, conn, h.cfg)
}

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

	if cfg.TLSConfig != nil {
		conn = tls.Client(conn, cfg.TLSConfig)
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

	// sessionCtx lives for the entire session lifetime and is cancelled on Close().
	sessionCtx, cancel := context.WithCancel(context.Background())

	grpcClient := muxpb.NewMuxClient(cc)

	// Open the Control stream with sessionCtx so it lives for the session lifetime.
	// (Using ctx here would cancel the stream when the caller's deadline expires.)
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
	mu        sync.RWMutex
	sess      *serverSession
	sessReady chan struct{} // closed once sess is set
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
func (svc *muxServer) Control(stream muxpb.Mux_ControlServer) error {
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

	if cfg.TLSConfig != nil {
		conn = tls.Server(conn, cfg.TLSConfig)
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
