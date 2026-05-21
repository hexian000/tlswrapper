// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import "sync/atomic"

// SessionMetrics tracks per-session stream counts plus payload and wire bytes.
type SessionMetrics struct {
	StreamsOpened      atomic.Uint64 // streams actively opened by this endpoint
	StreamsAccepted    atomic.Uint64 // streams passively accepted by this endpoint
	StreamsSucceeded   atomic.Uint64
	StreamsFailed      atomic.Uint64
	NumStreams         atomic.Int64
	BytesSent          atomic.Uint64
	BytesReceived      atomic.Uint64
	WireLengthSent     atomic.Uint64
	WireLengthReceived atomic.Uint64
}
