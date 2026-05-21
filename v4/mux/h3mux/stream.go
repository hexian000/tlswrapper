// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
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

// countingConn wraps a net.Conn and updates SessionMetrics on Close.
// Bytes are tracked directly via the embedded net.Conn's Read/Write by
// countingConn wrapping those calls.
type countingConn struct {
	net.Conn
	metrics *mux.SessionMetrics
	idleCh  chan<- struct{}
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.metrics != nil {
		c.metrics.BytesReceived.Add(uint64(n))
		c.metrics.WireLengthReceived.Add(uint64(n))
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 && c.metrics != nil {
		c.metrics.BytesSent.Add(uint64(n))
		c.metrics.WireLengthSent.Add(uint64(n))
	}
	return n, err
}

func (c *countingConn) Close() error {
	err := c.Conn.Close()
	if c.metrics != nil {
		if c.metrics.NumStreams.Add(-1) == 0 && c.idleCh != nil {
			select {
			case c.idleCh <- struct{}{}:
			default:
			}
		}
		// For QUIC, we can't distinguish success from failure on Close easily.
		// Treat all closes as successful.
		c.metrics.StreamsSucceeded.Add(1)
	}
	return err
}
