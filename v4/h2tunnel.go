// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"io"
	"net"
	"sync"
	"time"
)

// h2TunnelConn wraps an HTTP/2 request/response pair as a net.Conn for the client side.
// Write sends data as the outbound request body; Read receives data from the response body.
type h2TunnelConn struct {
	pw         *io.PipeWriter // write end → request body
	rb         io.ReadCloser  // response body → read from
	once       sync.Once
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newH2TunnelConn(pw *io.PipeWriter, rb io.ReadCloser, local, remote net.Addr) *h2TunnelConn {
	return &h2TunnelConn{pw: pw, rb: rb, localAddr: local, remoteAddr: remote}
}

func (c *h2TunnelConn) Read(b []byte) (int, error) {
	return c.rb.Read(b)
}

func (c *h2TunnelConn) Write(b []byte) (int, error) {
	return c.pw.Write(b)
}

func (c *h2TunnelConn) closeOnce() {
	c.once.Do(func() {
		_ = c.pw.Close()
		_ = c.rb.Close()
	})
}

func (c *h2TunnelConn) Close() error {
	c.closeOnce()
	return nil
}

func (c *h2TunnelConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *h2TunnelConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *h2TunnelConn) SetDeadline(t time.Time) error      { return nil }
func (c *h2TunnelConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *h2TunnelConn) SetWriteDeadline(t time.Time) error { return nil }

// responseBodyConn wraps an http.ResponseWriter + request body as a net.Conn for the server side.
// Write sends data to the client (response body); Read receives data from the client (request body).
type responseBodyConn struct {
	w          flushWriter
	rb         io.ReadCloser
	localAddr  net.Addr
	remoteAddr net.Addr
}

type flushWriter interface {
	io.Writer
	Flush()
}

func newResponseBodyConn(w flushWriter, rb io.ReadCloser, local, remote net.Addr) *responseBodyConn {
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

// Close is a no-op: the HTTP handler return will close the stream.
func (c *responseBodyConn) Close() error { return nil }

func (c *responseBodyConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *responseBodyConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *responseBodyConn) SetDeadline(t time.Time) error      { return nil }
func (c *responseBodyConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *responseBodyConn) SetWriteDeadline(t time.Time) error { return nil }

// h2Addr is a simple net.Addr implementation for HTTP/2 connections.
type h2Addr struct{ addr string }

func (a h2Addr) Network() string { return "tcp" }
func (a h2Addr) String() string  { return a.addr }
