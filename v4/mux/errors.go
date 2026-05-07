// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import "errors"

var (
	// ErrSessionClosed is returned by Accept and Open when the session has been closed.
	ErrSessionClosed = errors.New("session closed")

	// ErrInboundRejected is returned by Open when the peer advertised reject_inbound.
	ErrInboundRejected = errors.New("mux: peer rejects inbound streams")

	// ErrNoDeadline is returned when deadline operations are not supported.
	ErrNoDeadline = errors.New("deadline not supported")

	// ErrHandshakeFailed is returned by Server when the mux protocol handshake fails.
	ErrHandshakeFailed = errors.New("mux: handshake failed")

	errUnexpectedMessage = errors.New("mux: unexpected control message")
)
