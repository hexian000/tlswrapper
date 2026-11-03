// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import "sync/atomic"

// SessionMetrics tracks per-session stream counts plus payload and wire bytes.
type SessionMetrics struct {
	DatagramsSent         atomic.Uint64
	DatagramsReceived     atomic.Uint64
	DatagramBytesSent     atomic.Uint64
	DatagramBytesReceived atomic.Uint64
	DatagramsDropped      atomic.Uint64
	NumUDPFlows           atomic.Int64
	// LastDatagram is the Unix nanosecond timestamp of UDP session activity.
	LastDatagram atomic.Int64
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

// DatagramStats is a snapshot of locally observable UDP traffic. Sent means
// handed to the transport; neither QUIC delivery nor peer UDP delivery is implied.
type DatagramStats struct {
	DatagramsSent, DatagramsReceived         uint64
	DatagramBytesSent, DatagramBytesReceived uint64
	DatagramsDropped                         uint64
	NumUDPFlows                              int64
}

func (m *SessionMetrics) DatagramStats() DatagramStats {
	return DatagramStats{
		DatagramsSent: m.DatagramsSent.Load(), DatagramsReceived: m.DatagramsReceived.Load(),
		DatagramBytesSent: m.DatagramBytesSent.Load(), DatagramBytesReceived: m.DatagramBytesReceived.Load(),
		DatagramsDropped: m.DatagramsDropped.Load(), NumUDPFlows: m.NumUDPFlows.Load(),
	}
}

func (d *DatagramStats) Add(v DatagramStats) {
	d.DatagramsSent += v.DatagramsSent
	d.DatagramsReceived += v.DatagramsReceived
	d.DatagramBytesSent += v.DatagramBytesSent
	d.DatagramBytesReceived += v.DatagramBytesReceived
	d.DatagramsDropped += v.DatagramsDropped
	d.NumUDPFlows += v.NumUDPFlows
}
