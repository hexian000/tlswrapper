// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
)

type stubTCPConn struct {
	noDelay      bool
	keepAlive    bool
	rcvBuf       int
	sndBuf       int
	noDelayErr   error
	keepAliveErr error
	rcvBufErr    error
	sndBufErr    error
}

func (c *stubTCPConn) Read(b []byte) (int, error)       { return 0, nil }
func (c *stubTCPConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *stubTCPConn) Close() error                     { return nil }
func (c *stubTCPConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *stubTCPConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *stubTCPConn) SetDeadline(time.Time) error      { return nil }
func (c *stubTCPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stubTCPConn) SetWriteDeadline(time.Time) error { return nil }
func (c *stubTCPConn) SetNoDelay(v bool) error          { c.noDelay = v; return c.noDelayErr }
func (c *stubTCPConn) SetKeepAlive(v bool) error        { c.keepAlive = v; return c.keepAliveErr }
func (c *stubTCPConn) SetReadBuffer(bytes int) error    { c.rcvBuf = bytes; return c.rcvBufErr }
func (c *stubTCPConn) SetWriteBuffer(bytes int) error   { c.sndBuf = bytes; return c.sndBufErr }

type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }

func TestSetTCPConnParams(t *testing.T) {
	conn := &stubTCPConn{}
	setTCPConnParams(config.TCP{
		KeepAlive:   true,
		NoDelay:     true,
		ReadBuffer:  4096,
		WriteBuffer: 8192,
	}, conn)
	if !conn.noDelay {
		t.Fatal("NoDelay not applied")
	}
	if !conn.keepAlive {
		t.Fatal("KeepAlive not applied")
	}
	if conn.rcvBuf != 4096 {
		t.Fatalf("RcvBuf = %d, want 4096", conn.rcvBuf)
	}
	if conn.sndBuf != 8192 {
		t.Fatalf("SndBuf = %d, want 8192", conn.sndBuf)
	}
}

func TestSetTCPConnParamsLogsWarnings(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.Default()
	logger.SetLevel(slog.LevelWarning)
	logger.SetOutput(slog.OutputWriter, &buf)
	defer logger.SetOutput(slog.OutputDiscard)

	setTCPConnParams(config.TCP{
		KeepAlive:   true,
		NoDelay:     true,
		ReadBuffer:  4096,
		WriteBuffer: 8192,
	}, &stubTCPConn{
		noDelayErr:   errors.New("nodelay failed"),
		keepAliveErr: errors.New("keepalive failed"),
		rcvBufErr:    errors.New("read buffer failed"),
		sndBufErr:    errors.New("write buffer failed"),
	})

	got := buf.String()
	for _, want := range []string{
		"set tcp nodelay: nodelay failed",
		"set tcp keepalive: keepalive failed",
		"set tcp read buffer 4096: read buffer failed",
		"set tcp write buffer 8192: write buffer failed",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("log output %q does not contain %q", got, want)
		}
	}
}
