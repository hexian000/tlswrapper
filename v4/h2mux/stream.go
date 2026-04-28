// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"io"
	"net"
	"sync"
	"time"
)

// h2Addr is a simple net.Addr implementation for HTTP/2 connections.
type h2Addr struct{ Addr string }

func (a h2Addr) Network() string { return "tcp" }
func (a h2Addr) String() string  { return a.Addr }

// flushWriter is a writer that also supports flushing buffered data.
type flushWriter interface {
	io.Writer
	Flush()
}

// h2StreamConn wraps an HTTP/2 stream as a net.Conn for the client side.
// Write sends data as the outbound request body; Read receives data from the response body.
type h2StreamConn struct {
	pw         *io.PipeWriter // write end → request body
	rb         io.ReadCloser  // response body → read from
	once       sync.Once
	localAddr  net.Addr
	remoteAddr net.Addr
}

// newH2StreamConn wraps an HTTP/2 stream as a net.Conn for the client side.
func newH2StreamConn(pw *io.PipeWriter, rb io.ReadCloser, local, remote net.Addr) net.Conn {
	return &h2StreamConn{pw: pw, rb: rb, localAddr: local, remoteAddr: remote}
}

func (c *h2StreamConn) Read(b []byte) (int, error) {
	return c.rb.Read(b)
}

func (c *h2StreamConn) Write(b []byte) (int, error) {
	return c.pw.Write(b)
}

func (c *h2StreamConn) closeOnce() {
	c.once.Do(func() {
		_ = c.pw.Close()
		_ = c.rb.Close()
	})
}

func (c *h2StreamConn) Close() error {
	c.closeOnce()
	return nil
}

// CloseWrite closes the write end of the stream, signalling EOF to the peer
// while keeping the read end open to drain remaining data.
func (c *h2StreamConn) CloseWrite() error {
	return c.pw.Close()
}

func (c *h2StreamConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *h2StreamConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *h2StreamConn) SetDeadline(t time.Time) error      { return nil }
func (c *h2StreamConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *h2StreamConn) SetWriteDeadline(t time.Time) error { return nil }

// responseBodyConn wraps an http.ResponseWriter + request body as a net.Conn for the server side.
// Write sends data to the client (response body); Read receives data from the client (request body).
type responseBodyConn struct {
	w          flushWriter
	rb         io.ReadCloser
	localAddr  net.Addr
	remoteAddr net.Addr
}

// newResponseBodyConn wraps an http.ResponseWriter + request body as a net.Conn for the server side.
func newResponseBodyConn(w flushWriter, rb io.ReadCloser, local, remote net.Addr) net.Conn {
	return &responseBodyConn{w: w, rb: rb, localAddr: local, remoteAddr: remote}
}

func (c *responseBodyConn) Read(b []byte) (int, error) {
	return c.rb.Read(b)
}

func (c *responseBodyConn) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	if err == nil {
		c.w.Flush()
	}
	return n, err
}

// Close closes the request body, releasing resources held by the HTTP/2 stream.
func (c *responseBodyConn) Close() error { return c.rb.Close() }

func (c *responseBodyConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *responseBodyConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *responseBodyConn) SetDeadline(t time.Time) error      { return nil }
func (c *responseBodyConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *responseBodyConn) SetWriteDeadline(t time.Time) error { return nil }
