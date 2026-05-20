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
		"SetNoDelay: nodelay failed",
		"SetKeepAlive: keepalive failed",
		"SetReadBuffer 4096: read buffer failed",
		"SetWriteBuffer 8192: write buffer failed",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("log output %q does not contain %q", got, want)
		}
	}
}

// errCloser is an io.Closer that always returns the stored error.
type errCloser struct{ err error }

func (c errCloser) Close() error { return c.err }

func TestIoCloseError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.Default()
	logger.SetLevel(slog.LevelWarning)
	logger.SetOutput(slog.OutputWriter, &buf)
	defer logger.SetOutput(slog.OutputDiscard)

	ioClose(errCloser{errors.New("disk full")})

	if !bytes.Contains(buf.Bytes(), []byte("disk full")) {
		t.Fatalf("log output %q does not contain %q", buf.String(), "disk full")
	}
}

func TestIoCloseNoError(t *testing.T) {
	// Closing without error must not panic or log anything.
	ioClose(errCloser{nil})
}

func TestLatencyRingPercentilesEmpty(t *testing.T) {
	var r latencyRing
	_, _, _, _, ok := r.Percentiles()
	if ok {
		t.Fatal("Percentiles() ok = true on empty ring, want false")
	}
}

func TestLatencyRingPercentiles(t *testing.T) {
	var r latencyRing
	// Record 100 samples: 1 ms … 100 ms.
	for i := 1; i <= 100; i++ {
		r.Record(time.Duration(i) * time.Millisecond)
	}
	p50, p90, p99, pmax, ok := r.Percentiles()
	if !ok {
		t.Fatal("Percentiles() ok = false, want true")
	}
	if p50 <= 0 {
		t.Fatalf("p50 = %v, want > 0", p50)
	}
	if p90 < p50 {
		t.Fatalf("p90 (%v) < p50 (%v)", p90, p50)
	}
	if p99 < p90 {
		t.Fatalf("p99 (%v) < p90 (%v)", p99, p90)
	}
	if pmax < p99 {
		t.Fatalf("pmax (%v) < p99 (%v)", pmax, p99)
	}
	if pmax != 100*time.Millisecond {
		t.Fatalf("pmax = %v, want 100ms", pmax)
	}
}

func TestLatencyRingSnapshot(t *testing.T) {
	var r latencyRing
	r.Record(1 * time.Millisecond)
	r.Record(2 * time.Millisecond)
	snap := r.Snapshot()
	// At least one slot must be non-zero.
	nonZero := false
	for _, d := range snap {
		if d != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Fatal("Snapshot returned all zeros after recording samples")
	}
}
