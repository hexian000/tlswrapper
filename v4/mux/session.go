// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"net"
)

// Session is the public interface for a mux session over a single connection.
type Session interface {
	Open(ctx context.Context) (net.Conn, error)
	Accept() (net.Conn, error)
	Close() error
	IsClosed() bool
	CloseChan() <-chan struct{}
	// IdleChan returns a channel that receives a signal each time the number of
	// active streams drops to zero. May return nil when stats are unavailable.
	IdleChan() <-chan struct{}
	// Stats returns session statistics. Returns nil when unavailable.
	Stats() *SessionMetrics
	// PeerIdentity returns the remote identity claim.
	PeerIdentity() string
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// Dialer creates outbound mux sessions by dialing a remote address.
// Implementations must be safe for concurrent use.
type Dialer interface {
	Dial(ctx context.Context, addr string) (Session, error)
}

// Listener accepts inbound mux sessions.
// Implementations must be safe for concurrent use.
type Listener interface {
	AcceptSession(ctx context.Context) (Session, error)
	Addr() net.Addr
	Close() error
}
