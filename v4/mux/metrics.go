// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import "sync/atomic"

// SessionMetrics tracks per-session stream counts plus payload and wire bytes.
type SessionMetrics struct {
	// total streams opened by this endpoint (outbound)
	StreamsOpened atomic.Uint64
	// total streams accepted from the peer (inbound)
	StreamsAccepted atomic.Uint64
	// streams closed without error
	StreamsSucceeded atomic.Uint64
	// streams closed with an error
	StreamsFailed atomic.Uint64
	// current number of active streams
	NumStreams atomic.Int64
	// application-layer bytes sent across all streams
	BytesSent atomic.Uint64
	// application-layer bytes received across all streams
	BytesReceived atomic.Uint64
	// wire bytes sent (including protocol framing overhead)
	WireLengthSent atomic.Uint64
	// wire bytes received (including protocol framing overhead)
	WireLengthReceived atomic.Uint64
}
