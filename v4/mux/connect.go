// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
)

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
func Client(ctx context.Context, conn net.Conn, cfg *Config) (Session, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	if cfg.TLSConfig != nil {
		tlsConn := tls.Client(conn, cfg.TLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	}
	_ = conn.SetDeadline(time.Time{})

	dialAddr := conn.RemoteAddr().String()
	tag := fmt.Sprintf("? => %v", dialAddr)
	if cfg.LocalID != "" {
		tag = fmt.Sprintf("`%s' => %v", cfg.LocalID, dialAddr)
	}

	sh := &muxStatsHandler{}
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
		peerID             string
		peerRejectsInbound bool
		err                error
	}
	hsCh := make(chan hsResult, 1)
	go func() {
		peerID, peerRejectsInbound, err := doClientHandshake(ctrlStream, cfg.LocalID, cfg.RejectInbound)
		hsCh <- hsResult{peerID, peerRejectsInbound, err}
	}()

	var peerID string
	var peerRejectsInbound bool
	select {
	case res := <-hsCh:
		if res.err != nil {
			cancel()
			_ = cc.Close()
			return nil, fmt.Errorf("mux: handshake: %w", res.err)
		}
		peerID = res.peerID
		peerRejectsInbound = res.peerRejectsInbound
	case <-ctx.Done():
		cancel()
		_ = cc.Close()
		return nil, ctx.Err()
	}
	if peerID != "" {
		tag = fmt.Sprintf("`%s' => `%s'", cfg.LocalID, peerID)
	}

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
		peerID, tag,
		peerRejectsInbound,
		&sh.metrics,
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
	peerID, peerRejectsInbound, err := doServerHandshake(stream, svc.cfg.LocalID, svc.cfg.RejectInbound)
	if err != nil {
		return err
	}

	tag := fmt.Sprintf("? <= %v", svc.remoteAddr)
	if peerID != "" {
		tag = fmt.Sprintf("`%s' <= %v", peerID, svc.remoteAddr)
	}

	sess := newServerSession(
		stream,
		nil, // cleanup wired after grpcSrv reference is available in Server()
		svc.localAddr, svc.remoteAddr,
		peerID, tag,
		peerRejectsInbound,
		&svc.sh.metrics,
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

	conn := newServerSideStream(stream, svc.localAddr, svc.remoteAddr, nil)
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
func Server(ctx context.Context, conn net.Conn, cfg *Config) (Session, error) {
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

	sh := &muxStatsHandler{}
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
