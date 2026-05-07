// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
	"google.golang.org/grpc/metadata"
)

// ErrSessionClosed is returned by Accept and Open when the session has been closed.
var ErrSessionClosed = errors.New("session closed")

// ErrInboundRejected is returned by Open when the peer advertised reject_inbound.
var ErrInboundRejected = errors.New("mux: peer rejects inbound streams")

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
}

func (s *session) recvControlLoop() {
	for {
		msg, err := s.ctrl.Recv()
		if err != nil {
			return
		}
		switch body := msg.Body.(type) {
		case *muxpb.ControlMessage_OpenRequest:
			rid := body.OpenRequest.GetRequestId()
			if rid == "" || s.onOpenRequest == nil {
				continue
			}
			go s.onOpenRequest(rid)
		default:
			// ignore unexpected messages after handshake
		}
	}
}

// DeliverStream is called by the gRPC Stream handler (server side).
// requestID non-empty: route to pending Open() call; empty: push to acceptCh.
func (s *session) DeliverStream(requestID string, conn net.Conn) {
	if requestID != "" {
		s.pendingMu.Lock()
		ch := s.pending[requestID]
		s.pendingMu.Unlock()
		if ch != nil {
			s.numStreams.Add(1)
			select {
			case ch <- conn:
				return
			case <-s.closedCh:
				_ = conn.Close()
				return
			}
		}
	}
	s.numStreams.Add(1)
	select {
	case s.acceptCh <- conn:
	case <-s.closedCh:
		_ = conn.Close()
	}
}

// Accept waits for the next incoming stream.
func (s *session) Accept() (net.Conn, error) {
	select {
	case conn := <-s.acceptCh:
		return conn, nil
	case <-s.closedCh:
		select {
		case conn := <-s.acceptCh:
			return conn, nil
		default:
			return nil, ErrSessionClosed
		}
	}
}

// IsClosed reports whether the session has been closed.
func (s *session) IsClosed() bool {
	select {
	case <-s.closedCh:
		return true
	default:
		return false
	}
}

// CloseChan returns a channel that is closed when the session closes.
func (s *session) CloseChan() <-chan struct{} { return s.closedCh }

// NumStreams returns the current number of active streams.
func (s *session) NumStreams() int { return int(s.numStreams.Load()) }

// PeerID returns the remote service identity.
func (s *session) PeerID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.peerID
}

// Tag returns the human-readable session tag for logging.
func (s *session) Tag() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tag
}

// LocalAddr returns the local network address.
func (s *session) LocalAddr() net.Addr { return s.localAddr }

// RemoteAddr returns the remote network address.
func (s *session) RemoteAddr() net.Addr { return s.remoteAddr }

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
	peerRejectsInbound bool,
) *clientSession {
	if localAddr == nil {
		localAddr = h2Addr{"local"}
	}
	if remoteAddr == nil {
		remoteAddr = h2Addr{"remote"}
	}
	s := &clientSession{
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
		},
		grpcClient:      grpcClient,
		streamCtx:       streamCtx,
		streamCtxCancel: streamCtxCancel,
	}
	s.session.onOpenRequest = s.dialStreamForServer
	go func() {
		defer s.Close()
		s.session.recvControlLoop()
	}()
	return s
}

func (s *clientSession) dialStreamForServer(requestID string) {
	ctx := metadata.NewOutgoingContext(s.streamCtx, metadata.Pairs(metaRequestIDKey, requestID))
	cs, err := s.grpcClient.Stream(ctx)
	if err != nil {
		return
	}
	conn := newClientSideStream(cs, s.localAddr, s.remoteAddr, func() { s.numStreams.Add(-1) })
	s.numStreams.Add(1)
	select {
	case s.acceptCh <- conn:
	case <-s.closedCh:
		_ = conn.Close()
	}
}

// Open opens a new logical stream to the peer.
func (s *clientSession) Open(ctx context.Context) (net.Conn, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	if s.peerRejectsInbound {
		return nil, ErrInboundRejected
	}
	cs, err := s.grpcClient.Stream(s.streamCtx)
	if err != nil {
		return nil, err
	}
	s.numStreams.Add(1)
	return newClientSideStream(cs, s.localAddr, s.remoteAddr, func() { s.numStreams.Add(-1) }), nil
}

// Close shuts down the client session. Safe to call multiple times.
func (s *clientSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closedCh)
		s.streamCtxCancel()
		if s.cleanup != nil {
			s.cleanup()
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
) *serverSession {
	if localAddr == nil {
		localAddr = h2Addr{"local"}
	}
	if remoteAddr == nil {
		remoteAddr = h2Addr{"remote"}
	}
	s := &serverSession{
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
		},
	}
	go func() {
		defer s.Close()
		s.session.recvControlLoop()
	}()
	return s
}

// Open opens a new logical stream to the peer (server-side: sends OpenRequest and waits for client dial-back).
func (s *serverSession) Open(ctx context.Context) (net.Conn, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	if s.peerRejectsInbound {
		return nil, ErrInboundRejected
	}

	rid := strconv.FormatInt(s.openSeq.Add(1), 10)
	ch := make(chan net.Conn, 1)
	s.pendingMu.Lock()
	s.pending[rid] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, rid)
		s.pendingMu.Unlock()
	}()

	if err := s.ctrl.Send(&muxpb.ControlMessage{
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
	case <-s.closedCh:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down the server session. Safe to call multiple times.
func (s *serverSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closedCh)
		if s.cleanup != nil {
			s.cleanup()
		}
	})
	return nil
}
