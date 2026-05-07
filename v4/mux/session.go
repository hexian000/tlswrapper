// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
	"google.golang.org/grpc/metadata"
)

// metaRequestIDKey is the gRPC metadata key used by the client when opening a
// server-initiated stream. The value is the request_id from OpenRequest.
const metaRequestIDKey = "x-mux-request-id"

// Session is the public interface for a mux session over a single gRPC connection.
type Session interface {
	// Open opens a new logical stream to the peer.
	Open(ctx context.Context) (net.Conn, error)
	// Accept waits for the next incoming stream.
	Accept() (net.Conn, error)
	// Close shuts down the session. Safe to call multiple times.
	Close() error
	// IsClosed reports whether the session has been closed.
	IsClosed() bool
	// CloseChan returns a channel that is closed when the session closes.
	CloseChan() <-chan struct{}
	// NumStreams returns the current number of active streams.
	NumStreams() int
	// Metrics returns the gRPC transport statistics for this session.
	// Returns nil when stats collection is not available.
	Metrics() *SessionMetrics
	// PeerID returns the remote service identity.
	PeerID() string
	// Tag returns the human-readable session tag for logging.
	Tag() string
	// LocalAddr returns the local network address.
	LocalAddr() net.Addr
	// RemoteAddr returns the remote network address.
	RemoteAddr() net.Addr
}

// session holds the common state shared by clientSession and serverSession.
type session struct {
	ctrl controlStream

	// onOpenRequest is called (in a new goroutine) when a server-initiated
	// OpenRequest arrives. Set by clientSession constructor; nil on serverSession.
	onOpenRequest func(requestID string)

	pendingMu sync.Mutex
	pending   map[string]chan net.Conn // request_id -> delivery channel

	acceptCh chan net.Conn

	peerRejectsInbound bool

	mu         sync.RWMutex
	peerID     string
	tag        string
	localAddr  net.Addr
	remoteAddr net.Addr

	numStreams atomic.Int32
	openSeq    atomic.Int64
	closedCh   chan struct{}
	closeOnce  sync.Once
	cleanup    func()

	metrics *SessionMetrics
}

func (ss *session) recvControlLoop() {
	for {
		msg, err := ss.ctrl.Recv()
		if err != nil {
			return
		}
		switch body := msg.Body.(type) {
		case *muxpb.ControlMessage_OpenRequest:
			rid := body.OpenRequest.GetRequestId()
			if rid == "" || ss.onOpenRequest == nil {
				continue
			}
			go ss.onOpenRequest(rid)
		default:
			// ignore unexpected messages after handshake
		}
	}
}

// DeliverStream is called by the gRPC Stream handler (server side).
// requestID non-empty: route to pending Open() call; empty: push to acceptCh.
func (ss *session) DeliverStream(requestID string, conn net.Conn) {
	if requestID != "" {
		ss.pendingMu.Lock()
		ch := ss.pending[requestID]
		ss.pendingMu.Unlock()
		if ch != nil {
			ss.numStreams.Add(1)
			select {
			case ch <- conn:
				return
			case <-ss.closedCh:
				_ = conn.Close()
				return
			}
		}
	}
	ss.numStreams.Add(1)
	select {
	case ss.acceptCh <- conn:
	case <-ss.closedCh:
		_ = conn.Close()
	}
}

// Accept waits for the next incoming stream.
func (ss *session) Accept() (net.Conn, error) {
	select {
	case conn := <-ss.acceptCh:
		return conn, nil
	case <-ss.closedCh:
		select {
		case conn := <-ss.acceptCh:
			return conn, nil
		default:
			return nil, ErrSessionClosed
		}
	}
}

// IsClosed reports whether the session has been closed.
func (ss *session) IsClosed() bool {
	select {
	case <-ss.closedCh:
		return true
	default:
		return false
	}
}

// CloseChan returns a channel that is closed when the session closes.
func (ss *session) CloseChan() <-chan struct{} { return ss.closedCh }

// NumStreams returns the current number of active streams.
func (ss *session) NumStreams() int { return int(ss.numStreams.Load()) }

// Metrics returns the gRPC transport statistics for this session.
func (ss *session) Metrics() *SessionMetrics { return ss.metrics }

// PeerID returns the remote service identity.
func (ss *session) PeerID() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.peerID
}

// Tag returns the human-readable session tag for logging.
func (ss *session) Tag() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.tag
}

// LocalAddr returns the local network address.
func (ss *session) LocalAddr() net.Addr { return ss.localAddr }

// RemoteAddr returns the remote network address.
func (ss *session) RemoteAddr() net.Addr { return ss.remoteAddr }

// ---- clientSession ----

// clientSession is a mux session established from the client side (via Client()).
type clientSession struct {
	session
	grpcClient      muxpb.MuxClient
	streamCtx       context.Context
	streamCtxCancel context.CancelFunc
}

func newClientSession(
	ctrl controlStream,
	grpcClient muxpb.MuxClient,
	streamCtx context.Context,
	streamCtxCancel context.CancelFunc,
	cleanup func(),
	localAddr, remoteAddr net.Addr,
	peerID, tag string,
	peerRejectsInbound bool, metrics *SessionMetrics) *clientSession {
	if localAddr == nil {
		localAddr = h2Addr{"local"}
	}
	if remoteAddr == nil {
		remoteAddr = h2Addr{"remote"}
	}
	ss := &clientSession{
		session: session{
			ctrl:               ctrl,
			pending:            make(map[string]chan net.Conn),
			acceptCh:           make(chan net.Conn, 16),
			peerRejectsInbound: peerRejectsInbound,
			peerID:             peerID,
			tag:                tag,
			localAddr:          localAddr,
			remoteAddr:         remoteAddr,
			closedCh:           make(chan struct{}),
			cleanup:            cleanup,
			metrics:            metrics,
		},
		grpcClient:      grpcClient,
		streamCtx:       streamCtx,
		streamCtxCancel: streamCtxCancel,
	}
	ss.session.onOpenRequest = ss.dialStreamForServer
	go func() {
		defer ss.Close()
		ss.session.recvControlLoop()
	}()
	return ss
}

func (ss *clientSession) dialStreamForServer(requestID string) {
	ctx := metadata.NewOutgoingContext(ss.streamCtx, metadata.Pairs(metaRequestIDKey, requestID))
	cs, err := ss.grpcClient.Stream(ctx)
	if err != nil {
		return
	}
	conn := newClientSideStream(cs, ss.localAddr, ss.remoteAddr, func() { ss.numStreams.Add(-1) })
	ss.numStreams.Add(1)
	select {
	case ss.acceptCh <- conn:
	case <-ss.closedCh:
		_ = conn.Close()
	}
}

// Open opens a new logical stream to the peer.
func (ss *clientSession) Open(ctx context.Context) (net.Conn, error) {
	if ss.IsClosed() {
		return nil, ErrSessionClosed
	}
	if ss.peerRejectsInbound {
		return nil, ErrInboundRejected
	}
	cs, err := ss.grpcClient.Stream(ss.streamCtx)
	if err != nil {
		return nil, err
	}
	ss.numStreams.Add(1)
	return newClientSideStream(cs, ss.localAddr, ss.remoteAddr, func() { ss.numStreams.Add(-1) }), nil
}

// Close shuts down the client session. Safe to call multiple times.
func (ss *clientSession) Close() error {
	ss.closeOnce.Do(func() {
		close(ss.closedCh)
		ss.streamCtxCancel()
		if ss.cleanup != nil {
			ss.cleanup()
		}
	})
	return nil
}

// ---- serverSession ----

// serverSession is a mux session established from the server side (via Server()).
type serverSession struct {
	session
}

func newServerSession(
	ctrl controlStream,
	cleanup func(),
	localAddr, remoteAddr net.Addr,
	peerID, tag string,
	peerRejectsInbound bool,
	metrics *SessionMetrics,
) *serverSession {
	if localAddr == nil {
		localAddr = h2Addr{"local"}
	}
	if remoteAddr == nil {
		remoteAddr = h2Addr{"remote"}
	}
	ss := &serverSession{
		session: session{
			ctrl:               ctrl,
			pending:            make(map[string]chan net.Conn),
			acceptCh:           make(chan net.Conn, 16),
			peerRejectsInbound: peerRejectsInbound,
			peerID:             peerID,
			tag:                tag,
			localAddr:          localAddr,
			remoteAddr:         remoteAddr,
			closedCh:           make(chan struct{}),
			cleanup:            cleanup,
			metrics:            metrics,
		},
	}
	go func() {
		defer ss.Close()
		ss.session.recvControlLoop()
	}()
	return ss
}

// Open opens a new logical stream to the peer (server-side: sends OpenRequest and waits for client dial-back).
func (ss *serverSession) Open(ctx context.Context) (net.Conn, error) {
	if ss.IsClosed() {
		return nil, ErrSessionClosed
	}
	if ss.peerRejectsInbound {
		return nil, ErrInboundRejected
	}

	rid := strconv.FormatInt(ss.openSeq.Add(1), 10)
	ch := make(chan net.Conn, 1)
	ss.pendingMu.Lock()
	ss.pending[rid] = ch
	ss.pendingMu.Unlock()
	defer func() {
		ss.pendingMu.Lock()
		delete(ss.pending, rid)
		ss.pendingMu.Unlock()
	}()

	if err := ss.ctrl.Send(&muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_OpenRequest{
			OpenRequest: &muxpb.OpenRequest{RequestId: rid},
		},
	}); err != nil {
		return nil, err
	}

	select {
	case conn, ok := <-ch:
		if !ok {
			return nil, ErrSessionClosed
		}
		return conn, nil
	case <-ss.closedCh:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down the server session. Safe to call multiple times.
func (ss *serverSession) Close() error {
	ss.closeOnce.Do(func() {
		close(ss.closedCh)
		if ss.cleanup != nil {
			ss.cleanup()
		}
	})
	return nil
}
