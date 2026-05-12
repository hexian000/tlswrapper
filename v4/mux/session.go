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
	Open(ctx context.Context) (net.Conn, error)
	Accept() (net.Conn, error)
	Close() error
	IsClosed() bool
	CloseChan() <-chan struct{}
	// Stats returns the gRPC transport statistics for this session.
	// Returns nil when stats collection is not available.
	Stats() *SessionMetrics
	// PeerID returns the remote identity claim.
	PeerID() string
	LocalAddr() net.Addr
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
	localAddr  net.Addr
	remoteAddr net.Addr

	openSeq   atomic.Uint64
	closedCh  chan struct{}
	closeOnce sync.Once
	cleanup   func()

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
			select {
			case ch <- conn:
				return
			case <-ss.closedCh:
				_ = conn.Close()
				return
			}
		}
	}
	select {
	case ss.acceptCh <- conn:
		if ss.metrics != nil {
			ss.metrics.StreamsAccepted.Add(1)
		}
	case <-ss.closedCh:
		_ = conn.Close()
	}
}

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

func (ss *session) IsClosed() bool {
	select {
	case <-ss.closedCh:
		return true
	default:
		return false
	}
}

func (ss *session) CloseChan() <-chan struct{} { return ss.closedCh }

func (ss *session) Stats() *SessionMetrics { return ss.metrics }

// PeerID returns the remote identity claim.
func (ss *session) PeerID() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.peerID
}

func (ss *session) LocalAddr() net.Addr { return ss.localAddr }

func (ss *session) RemoteAddr() net.Addr { return ss.remoteAddr }

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
	peerID string,
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
	streamCtx, streamCancel := context.WithCancel(ss.streamCtx)
	ctx := metadata.NewOutgoingContext(streamCtx, metadata.Pairs(metaRequestIDKey, requestID))
	cs, err := ss.grpcClient.Stream(ctx)
	if err != nil {
		streamCancel()
		return
	}
	conn := newClientSideStream(cs, ss.localAddr, ss.remoteAddr, streamCancel, streamCancel)
	select {
	case ss.acceptCh <- conn:
		if ss.metrics != nil {
			ss.metrics.StreamsAccepted.Add(1)
		}
	case <-ss.closedCh:
		_ = conn.Close()
	}
}

func (ss *clientSession) Open(ctx context.Context) (net.Conn, error) {
	if ss.IsClosed() {
		return nil, ErrSessionClosed
	}
	if ss.peerRejectsInbound {
		return nil, ErrInboundRejected
	}
	streamCtx, streamCancel := context.WithCancel(ss.streamCtx)
	cs, err := ss.grpcClient.Stream(streamCtx)
	if err != nil {
		streamCancel()
		return nil, err
	}
	if ss.metrics != nil {
		ss.metrics.StreamsOpened.Add(1)
	}
	return newClientSideStream(cs, ss.localAddr, ss.remoteAddr, streamCancel, streamCancel), nil
}

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

type serverSession struct {
	session
}

func newServerSession(
	ctrl controlStream,
	cleanup func(),
	localAddr, remoteAddr net.Addr,
	peerID string,
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

// Open asks the client to dial back a Stream RPC and waits for delivery.
func (ss *serverSession) Open(ctx context.Context) (net.Conn, error) {
	if ss.IsClosed() {
		return nil, ErrSessionClosed
	}
	if ss.peerRejectsInbound {
		return nil, ErrInboundRejected
	}

	rid := strconv.FormatUint(ss.openSeq.Add(1), 10)
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
		if ss.metrics != nil {
			ss.metrics.StreamsOpened.Add(1)
		}
		return conn, nil
	case <-ss.closedCh:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (ss *serverSession) Close() error {
	ss.closeOnce.Do(func() {
		close(ss.closedCh)
		if ss.cleanup != nil {
			ss.cleanup()
		}
	})
	return nil
}
