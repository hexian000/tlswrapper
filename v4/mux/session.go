// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// Session represents a mux session over a single gRPC connection.
type Session struct {
	ctrl            controlStream
	grpcClient      muxpb.MuxClient    // non-nil on client side only
	streamCtx       context.Context    // client-side: context for Stream RPCs
	streamCtxCancel context.CancelFunc // client-side: cancels all Stream RPCs on Close

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

func newClientSession(
	ctrl controlStream,
	grpcClient muxpb.MuxClient,
	streamCtx context.Context,
	streamCtxCancel context.CancelFunc,
	cleanup func(),
	localAddr, remoteAddr net.Addr,
	peerID, tag string,
	peerRejectsInbound bool,
) *Session {
	if localAddr == nil {
		localAddr = h2Addr{"local"}
	}
	if remoteAddr == nil {
		remoteAddr = h2Addr{"remote"}
	}
	s := &Session{
		ctrl:               ctrl,
		grpcClient:         grpcClient,
		streamCtx:          streamCtx,
		streamCtxCancel:    streamCtxCancel,
		cleanup:            cleanup,
		pending:            make(map[string]chan net.Conn),
		acceptCh:           make(chan net.Conn, 16),
		peerRejectsInbound: peerRejectsInbound,
		peerID:             peerID,
		tag:                tag,
		localAddr:          localAddr,
		remoteAddr:         remoteAddr,
		closedCh:           make(chan struct{}),
	}
	go s.recvControlLoop()
	return s
}

func newServerSession(
	ctrl controlStream,
	cleanup func(),
	localAddr, remoteAddr net.Addr,
	peerID, tag string,
	peerRejectsInbound bool,
) *Session {
	if localAddr == nil {
		localAddr = h2Addr{"local"}
	}
	if remoteAddr == nil {
		remoteAddr = h2Addr{"remote"}
	}
	s := &Session{
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
	}
	go s.recvControlLoop()
	return s
}

func (s *Session) recvControlLoop() {
	defer s.Close()
	for {
		msg, err := s.ctrl.Recv()
		if err != nil {
			return
		}
		switch body := msg.Body.(type) {
		case *muxpb.ControlMessage_OpenRequest:
			rid := body.OpenRequest.GetRequestId()
			if rid == "" || s.grpcClient == nil {
				continue
			}
			go s.dialStreamForServer(rid)
		default:
			// ignore unexpected messages after handshake
		}
	}
}

func (s *Session) dialStreamForServer(requestID string) {
	ctx := metadata.NewOutgoingContext(s.streamCtx, metadata.Pairs(metaRequestIDKey, requestID))
	cs, err := s.grpcClient.Stream(ctx)
	if err != nil {
		return
	}
	conn := newClientSideStream(cs, s.localAddr, s.remoteAddr)
	s.numStreams.Add(1)
	select {
	case s.acceptCh <- conn:
	case <-s.closedCh:
		_ = conn.Close()
	}
}

// DeliverStream is called by the gRPC Stream handler (server side).
// requestID non-empty: route to pending Open() call; empty: push to acceptCh.
func (s *Session) DeliverStream(requestID string, conn net.Conn) {
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

// Open opens a new logical stream to the peer.
func (s *Session) Open(ctx context.Context) (net.Conn, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	if s.peerRejectsInbound {
		return nil, ErrInboundRejected
	}

	if s.grpcClient != nil {
		cs, err := s.grpcClient.Stream(s.streamCtx)
		if err != nil {
			return nil, err
		}
		s.numStreams.Add(1)
		return newClientSideStream(cs, s.localAddr, s.remoteAddr), nil
	}

	// Server side: send OpenRequest and wait for client to dial back.
	rid := fmt.Sprintf("%d", s.openSeq.Add(1))
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

// Accept waits for the next incoming stream.
func (s *Session) Accept() (net.Conn, error) {
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

// Close shuts down the session. Safe to call multiple times.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		close(s.closedCh)
		if s.streamCtxCancel != nil {
			s.streamCtxCancel()
		}
		if s.cleanup != nil {
			s.cleanup()
		}
	})
	return nil
}

// IsClosed reports whether the session has been closed.
func (s *Session) IsClosed() bool {
	select {
	case <-s.closedCh:
		return true
	default:
		return false
	}
}

// CloseChan returns a channel that is closed when the session closes.
func (s *Session) CloseChan() <-chan struct{} { return s.closedCh }

// NumStreams returns the monotonically increasing count of streams opened.
func (s *Session) NumStreams() int { return int(s.numStreams.Load()) }

// PeerID returns the remote service identity.
func (s *Session) PeerID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.peerID
}

// Tag returns the human-readable session tag for logging.
func (s *Session) Tag() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tag
}

// LocalAddr returns the local network address.
func (s *Session) LocalAddr() net.Addr { return s.localAddr }

// RemoteAddr returns the remote network address.
func (s *Session) RemoteAddr() net.Addr { return s.remoteAddr }
