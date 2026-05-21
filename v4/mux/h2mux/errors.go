// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import "errors"

var (
	// ErrInboundRejected is returned by Open when the peer advertised reject_inbound.
	ErrInboundRejected = errors.New("mux: peer rejects inbound streams")

	// ErrHandshakeFailed is returned by Server when the mux protocol handshake fails.
	ErrHandshakeFailed = errors.New("mux: handshake failed")

	errUnexpectedMessage = errors.New("mux: unexpected control message")
)
