// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"io"
	"net"
	"sync/atomic"

	"github.com/quic-go/quic-go"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// compile-time check that quicConn implements net.Conn.
var _ net.Conn = (*quicConn)(nil)

// quicConn wraps quic.Stream (interface), adding the LocalAddr and RemoteAddr
// methods required by net.Conn (which quic.Stream itself does not provide).
type quicConn struct {
	quic.Stream
	localAddr  net.Addr
	remoteAddr net.Addr

	// onClose is called exactly once when Close is called.
	onClose   func(err error)
	closeOnce atomic.Bool
}

func (c *quicConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *quicConn) RemoteAddr() net.Addr { return c.remoteAddr }

// Close closes the send direction of the stream and fires the onClose callback.
func (c *quicConn) Close() error {
	err := c.Stream.Close()
	if c.closeOnce.CompareAndSwap(false, true) {
		if c.onClose != nil {
			c.onClose(err)
		}
	}
	return err
}

// newQuicConn wraps a QUIC stream as a net.Conn.
// onClose is called (exactly once) when Close() is first called; it may be nil.
func newQuicConn(s quic.Stream, localAddr, remoteAddr net.Addr, onClose func(err error)) *quicConn {
	return &quicConn{
		Stream:     s,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		onClose:    onClose,
	}
}

// countingConn wraps a net.Conn and updates SessionMetrics on Read/Write/Close.
// BytesSent/BytesReceived track application-layer bytes.
// WireLengthSent/WireLengthReceived are left at zero: QUIC framing and
// encryption overhead are not accessible at this level.
// StreamsSucceeded/StreamsFailed are updated on Close based on whether any
// Read or Write returned a non-EOF error during the stream's lifetime.
type countingConn struct {
	net.Conn
	metrics *mux.SessionMetrics
	errored atomic.Bool
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.metrics != nil {
		c.metrics.BytesReceived.Add(uint64(n))
	}
	if err != nil && err != io.EOF {
		c.errored.Store(true)
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 && c.metrics != nil {
		c.metrics.BytesSent.Add(uint64(n))
	}
	if err != nil {
		c.errored.Store(true)
	}
	return n, err
}

func (c *countingConn) Close() error {
	err := c.Conn.Close()
	// NumStreams decrement and idle signalling are handled by the onStreamClose
	// callback attached to the underlying quicConn; do not repeat them here.
	if c.metrics != nil {
		if c.errored.Load() {
			c.metrics.StreamsFailed.Add(1)
		} else {
			c.metrics.StreamsSucceeded.Add(1)
		}
	}
	return err
}
