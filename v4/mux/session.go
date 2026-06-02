// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"net"
)

// Session is the public interface for a mux session over a single connection.
//
// Handshake runs the protocol-level handshake if it has not already been done.
// Like crypto/tls.Conn.HandshakeContext it is idempotent and safe for concurrent
// use. Methods that require an established session (Open, Accept) call Handshake
// implicitly using context.Background() when invoked on a pre-handshake session.
type Session interface {
	// Handshake completes the protocol setup. It is a no-op after the first
	// successful call, and returns the same error for every call after a failure.
	Handshake(ctx context.Context) error
	// Open opens a new outbound stream to the peer.
	Open(ctx context.Context) (net.Conn, error)
	// Accept blocks until a new inbound stream arrives or the session is closed.
	Accept() (net.Conn, error)
	// Close closes the session and all its streams.
	Close() error
	// IsClosed reports whether the session has been closed.
	IsClosed() bool
	// CloseChan returns a channel that is closed when the session is closed.
	CloseChan() <-chan struct{}
	// IdleChan returns a channel that receives a signal each time the number of
	// active streams drops to zero. May return nil when stats are unavailable.
	IdleChan() <-chan struct{}
	// Stats returns session statistics. Returns nil when unavailable.
	Stats() *SessionMetrics
	// PeerIdentity returns the remote identity claim.
	PeerIdentity() string
	// LocalAddr returns the local network address.
	LocalAddr() net.Addr
	// RemoteAddr returns the remote network address.
	RemoteAddr() net.Addr
}

// Dialer creates outbound mux sessions by dialing a remote address.
// Implementations must be safe for concurrent use.
type Dialer interface {
	// Dial connects to addr and returns an established Session.
	Dial(ctx context.Context, addr string) (Session, error)
}

// Listener accepts inbound mux sessions.
// Implementations must be safe for concurrent use.
type Listener interface {
	// Accept waits for and returns a new inbound Session whose Handshake has
	// NOT yet been run. Call Session.Handshake to complete the setup, or use
	// Open/Accept (stream) which trigger it implicitly with context.Background().
	Accept() (Session, error)
	// Addr returns the listener's local network address.
	Addr() net.Addr
	// Close closes the listener and causes any blocked Accept to return an error.
	Close() error
}
