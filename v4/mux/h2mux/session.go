// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	mux "github.com/hexian000/tlswrapper/v4/mux"
	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
	"google.golang.org/grpc/metadata"
)

// metaRequestIDKey is the gRPC metadata key used by the client when opening a
// server-initiated stream. The value is the request_id from OpenRequest.
const metaRequestIDKey = "x-mux-request-id"

// compile-time checks that clientSession and serverSession implement mux.Session.
var _ mux.Session = (*clientSession)(nil)
var _ mux.Session = (*serverSession)(nil)

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

	mu           sync.RWMutex
	peerIdentity string
	localAddr    net.Addr
	remoteAddr   net.Addr

	openSeq   atomic.Uint64
	closedCh  chan struct{}
	closeOnce sync.Once
	cleanup   func()

	metrics    *mux.SessionMetrics
	idleNotify <-chan struct{}
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
		// Claim the waiter by removing it under the lock: this makes delivery and
		// the waiter's abandon (see serverSession.Open) mutually exclusive, so a
		// given request is resolved by exactly one of them.
		ss.pendingMu.Lock()
		ch := ss.pending[requestID]
		if ch != nil {
			delete(ss.pending, requestID)
		}
		ss.pendingMu.Unlock()
		if ch == nil {
			// No waiter: the Open() call already gave up (timeout/close), or the
			// requestID is unknown or a duplicate. Close the conn instead of
			// misrouting it onto acceptCh as a peer-initiated inbound stream.
			_ = conn.Close()
			return
		}
		// We exclusively own this handoff. ch is buffered (cap 1) with this as
		// its only sender, so the send never blocks. The waiter either receives
		// the conn or, if it has already abandoned, drains and closes it.
		ch <- conn
		return
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
			return nil, mux.ErrSessionClosed
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

func (ss *session) IdleChan() <-chan struct{} { return ss.idleNotify }

func (ss *session) Stats() *mux.SessionMetrics { return ss.metrics }

// PeerIdentity returns the remote identity claim.
func (ss *session) PeerIdentity() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.peerIdentity
}

func (ss *session) LocalAddr() net.Addr { return ss.localAddr }

func (ss *session) RemoteAddr() net.Addr { return ss.remoteAddr }

// Handshake is a no-op: clientSession and serverSession are already established.
func (ss *session) Handshake(_ context.Context) error { return nil }

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
	peerIdentity string,
	peerRejectsInbound bool,
	metrics *mux.SessionMetrics,
	idleNotify <-chan struct{}) *clientSession {
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
			peerIdentity:       peerIdentity,
			localAddr:          localAddr,
			remoteAddr:         remoteAddr,
			closedCh:           make(chan struct{}),
			cleanup:            cleanup,
			metrics:            metrics,
			idleNotify:         idleNotify,
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
		return nil, mux.ErrSessionClosed
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
	peerIdentity string,
	peerRejectsInbound bool,
	metrics *mux.SessionMetrics,
	idleNotify <-chan struct{},
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
			peerIdentity:       peerIdentity,
			localAddr:          localAddr,
			remoteAddr:         remoteAddr,
			closedCh:           make(chan struct{}),
			cleanup:            cleanup,
			metrics:            metrics,
			idleNotify:         idleNotify,
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
		return nil, mux.ErrSessionClosed
	}
	if ss.peerRejectsInbound {
		return nil, ErrInboundRejected
	}

	rid := strconv.FormatUint(ss.openSeq.Add(1), 10)
	ch := make(chan net.Conn, 1)
	ss.pendingMu.Lock()
	ss.pending[rid] = ch
	ss.pendingMu.Unlock()

	// abandon removes our pending entry when we give up (send error, ctx done,
	// or session close). It returns claimed=true when a deliverer has already
	// removed the entry, in which case that deliverer is guaranteed to send
	// exactly one conn on ch; the caller must then drain and close it so the
	// stream is not leaked.
	abandon := func() (claimed bool) {
		ss.pendingMu.Lock()
		defer ss.pendingMu.Unlock()
		if ss.pending[rid] == ch {
			delete(ss.pending, rid)
			return false
		}
		return true
	}

	if err := ss.ctrl.Send(&muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_OpenRequest{
			OpenRequest: &muxpb.OpenRequest{RequestId: rid},
		},
	}); err != nil {
		if abandon() {
			_ = (<-ch).Close()
		}
		return nil, err
	}

	select {
	case conn := <-ch:
		if ss.metrics != nil {
			ss.metrics.StreamsOpened.Add(1)
		}
		return conn, nil
	case <-ss.closedCh:
		if abandon() {
			_ = (<-ch).Close()
		}
		return nil, mux.ErrSessionClosed
	case <-ctx.Done():
		if abandon() {
			_ = (<-ch).Close()
		}
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
