// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"

	"github.com/quic-go/quic-go"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// compile-time check that quicConn implements net.Conn.
var _ net.Conn = (*quicConn)(nil)

// quicConn wraps *quic.Stream (struct pointer), adding the LocalAddr and RemoteAddr
// methods required by net.Conn (which quic.Stream itself does not provide).
type quicConn struct {
	*quic.Stream
	localAddr  net.Addr
	remoteAddr net.Addr

	// stripMarker is set on accepted streams: the 1-byte open marker written
	// by the opener's Open() is consumed before the first application read.
	// Doing this lazily (instead of in Accept) keeps the session-level accept
	// loop from blocking on a stream whose marker has not arrived yet.
	stripMarker bool

	// onClose is called exactly once when Close is called.
	onClose   func(err error)
	closeOnce atomic.Bool
}

func (c *quicConn) Read(b []byte) (int, error) {
	if c.stripMarker {
		var marker [1]byte
		if _, err := io.ReadFull(c.Stream, marker[:]); err != nil {
			return 0, fmt.Errorf("open marker read: %w", err)
		}
		c.stripMarker = false
	}
	return c.Stream.Read(b)
}

func (c *quicConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *quicConn) RemoteAddr() net.Addr { return c.remoteAddr }

// CloseWrite half-closes the send direction (sends FIN); reads continue.
// quic.Stream.Close only closes the send side, matching CloseWrite semantics.
func (c *quicConn) CloseWrite() error {
	return c.Stream.Close()
}

// Close fully terminates the stream: it closes the send direction and stops
// receiving (matching net.TCPConn.Close semantics), then fires onClose once.
func (c *quicConn) Close() error {
	c.Stream.CancelRead(0)
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
func newQuicConn(s *quic.Stream, localAddr, remoteAddr net.Addr, onClose func(err error)) *quicConn {
	return &quicConn{
		Stream:     s,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		onClose:    onClose,
	}
}

// countingConn wraps a net.Conn and updates SessionMetrics on Read/Write/Close.
// BytesSent/BytesReceived track application-layer bytes.
// WireLengthSent/WireLengthReceived are not touched here: they are accumulated
// at the QUIC packet level by the wire tracer (see wiretrace.go).
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

// CloseWrite half-closes the write side when the underlying conn supports it
// (quicConn always does); otherwise it falls back to a full Close.
func (c *countingConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
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
