// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// compile-time check that h3Session implements mux.Session.
var _ mux.Session = (*h3Session)(nil)

// openMarker is a 1-byte stream-open signal written by Open() and consumed by
// Accept(). QUIC streams are invisible to the peer until the first STREAM frame
// is sent, so without this handshake Accept() would block forever if the caller
// waits for it before writing any application data (unlike gRPC/h2mux, which
// sends HTTP/2 HEADERS when opening a stream).
var openMarker = [1]byte{0}

// h3Session implements mux.Session over a QUIC connection.
// Because QUIC is symmetric (both endpoints can open streams), there is no
// distinction between client and server sessions.
type h3Session struct {
	conn            *quic.Conn
	ctrlStream      *quic.Stream
	cfg             *Config
	peerIdentity    string
	peerRejectsOpen bool // peer advertised RejectInbound → we must not Open()

	closedCh  chan struct{}
	closeOnce sync.Once
	metrics   mux.SessionMetrics
	idleCh    chan struct{}
}

// newH3Session creates an h3Session and starts its lifecycle goroutine.
// conn and ctrl must both be alive. ctrl is the control stream (stream 0).
func newH3Session(conn *quic.Conn, ctrl *quic.Stream, peerIdentity string, peerRejectsOpen bool, cfg *Config) *h3Session {
	s := &h3Session{
		conn:            conn,
		ctrlStream:      ctrl,
		cfg:             cfg,
		peerIdentity:    peerIdentity,
		peerRejectsOpen: peerRejectsOpen,
		closedCh:        make(chan struct{}),
		idleCh:          make(chan struct{}, 1),
	}
	go s.run()
	return s
}

// run monitors the QUIC connection context. When the connection is closed
// (by the peer or by idle timeout), we reflect that into closedCh.
func (s *h3Session) run() {
	select {
	case <-s.conn.Context().Done():
		s.close()
	case <-s.closedCh:
		// already closed by our side
	}
}

// close is the internal close: idempotent, safe to call from any goroutine.
func (s *h3Session) close() {
	s.closeOnce.Do(func() {
		close(s.closedCh)
		_ = s.ctrlStream.Close()
		_ = s.conn.CloseWithError(0, "")
	})
}

// onStreamClose is the callback attached to each wrapped stream.
// It decrements NumStreams and, if it reaches zero, signals IdleChan.
func (s *h3Session) onStreamClose(_ error) {
	if n := s.metrics.NumStreams.Add(-1); n == 0 {
		select {
		case s.idleCh <- struct{}{}:
		default:
		}
	}
}

// wrapStream wraps a raw quic.Stream into a net.Conn-compatible countingConn.
func (s *h3Session) wrapStream(qs *quic.Stream) net.Conn {
	local := s.conn.LocalAddr()
	remote := s.conn.RemoteAddr()
	inner := newQuicConn(qs, local, remote, s.onStreamClose)
	return &countingConn{
		Conn:    inner,
		metrics: &s.metrics,
	}
}

// Open opens a new bidirectional stream to the peer.
// A 1-byte open marker is written immediately so that the peer's Accept()
// returns as soon as the stream is created, before any application data is
// sent. This mirrors the behaviour of gRPC/h2mux, which sends HTTP/2 HEADERS
// on stream open. Without this, Accept() on the peer would block until the
// first application Write(), because QUIC streams are not announced to the
// peer until the first STREAM frame is transmitted.
func (s *h3Session) Open(ctx context.Context) (net.Conn, error) {
	if s.IsClosed() {
		return nil, mux.ErrSessionClosed
	}
	if s.peerRejectsOpen {
		return nil, ErrInboundRejected
	}
	qs, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		s.metrics.StreamsFailed.Add(1)
		if s.IsClosed() {
			return nil, mux.ErrSessionClosed
		}
		return nil, err
	}
	if _, err := qs.Write(openMarker[:]); err != nil {
		_ = qs.Close()
		s.metrics.StreamsFailed.Add(1)
		if s.IsClosed() {
			return nil, mux.ErrSessionClosed
		}
		return nil, fmt.Errorf("open marker write: %w", err)
	}
	s.metrics.StreamsOpened.Add(1)
	s.metrics.NumStreams.Add(1)
	return s.wrapStream(qs), nil
}

// Accept blocks until a new stream is available or the session is closed.
// It reads and discards the 1-byte open marker written by the opener's Open().
func (s *h3Session) Accept() (net.Conn, error) {
	qs, err := s.conn.AcceptStream(s.conn.Context())
	if err != nil {
		if s.IsClosed() {
			return nil, mux.ErrSessionClosed
		}
		return nil, err
	}
	var marker [1]byte
	if _, err := io.ReadFull(qs, marker[:]); err != nil {
		_ = qs.Close()
		if s.IsClosed() {
			return nil, mux.ErrSessionClosed
		}
		return nil, fmt.Errorf("open marker read: %w", err)
	}
	s.metrics.StreamsAccepted.Add(1)
	s.metrics.NumStreams.Add(1)
	return s.wrapStream(qs), nil
}

// Close closes the session and the underlying QUIC connection.
func (s *h3Session) Close() error {
	s.close()
	return nil
}

// IsClosed reports whether the session has been closed.
func (s *h3Session) IsClosed() bool {
	select {
	case <-s.closedCh:
		return true
	default:
		return false
	}
}

// CloseChan returns a channel that is closed when the session is closed.
func (s *h3Session) CloseChan() <-chan struct{} { return s.closedCh }

// IdleChan returns a channel that receives a signal each time NumStreams drops to 0.
func (s *h3Session) IdleChan() <-chan struct{} { return s.idleCh }

// Stats returns the session's live metrics.
func (s *h3Session) Stats() *mux.SessionMetrics { return &s.metrics }

// PeerIdentity returns the remote identity claim from the handshake.
func (s *h3Session) PeerIdentity() string { return s.peerIdentity }

// LocalAddr returns the local QUIC address.
func (s *h3Session) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// RemoteAddr returns the remote QUIC address.
func (s *h3Session) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }

// Handshake is a no-op: h3Session is already established.
func (s *h3Session) Handshake(_ context.Context) error { return nil }
