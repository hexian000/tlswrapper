// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type stubAddr struct{}

func (stubAddr) Network() string { return "stub" }
func (stubAddr) String() string  { return "stub" }

type stubConn struct {
	deadlines        []time.Time
	setDeadlineErr   error
	writeErr         error
	writeN           int
	writeCalls       int
	failFirstSetOnly bool
}

func (c *stubConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *stubConn) Close() error             { return nil }
func (c *stubConn) LocalAddr() net.Addr      { return stubAddr{} }
func (c *stubConn) RemoteAddr() net.Addr     { return stubAddr{} }
func (c *stubConn) SetDeadline(time.Time) error {
	return nil
}
func (c *stubConn) SetReadDeadline(time.Time) error {
	return nil
}
func (c *stubConn) SetWriteDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	if c.setDeadlineErr == nil {
		return nil
	}
	if !c.failFirstSetOnly || len(c.deadlines) == 1 {
		return c.setDeadlineErr
	}
	return nil
}
func (c *stubConn) Write([]byte) (int, error) {
	c.writeCalls++
	if c.writeN == 0 {
		c.writeN = 1
	}
	return c.writeN, c.writeErr
}

func TestWriteTimeoutConnWriteSuccess(t *testing.T) {
	conn := &stubConn{writeN: 3}
	w := &writeTimeoutConn{Conn: conn, timeout: 50 * time.Millisecond}

	n, err := w.Write([]byte("abc"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 3 {
		t.Fatalf("Write() n = %d, want 3", n)
	}
	if conn.writeCalls != 1 {
		t.Fatalf("writeCalls = %d, want 1", conn.writeCalls)
	}
	if len(conn.deadlines) != 2 {
		t.Fatalf("deadline calls = %d, want 2", len(conn.deadlines))
	}
	if conn.deadlines[0].IsZero() {
		t.Fatal("first deadline should be non-zero")
	}
	if !conn.deadlines[1].IsZero() {
		t.Fatal("second deadline should clear to zero")
	}
}

func TestWriteTimeoutConnWriteSetDeadlineError(t *testing.T) {
	wantErr := errors.New("set deadline failed")
	conn := &stubConn{setDeadlineErr: wantErr, failFirstSetOnly: true}
	w := &writeTimeoutConn{Conn: conn, timeout: 50 * time.Millisecond}

	n, err := w.Write([]byte("abc"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write() error = %v, want %v", err, wantErr)
	}
	if n != 0 {
		t.Fatalf("Write() n = %d, want 0", n)
	}
	if conn.writeCalls != 0 {
		t.Fatalf("writeCalls = %d, want 0", conn.writeCalls)
	}
	if len(conn.deadlines) != 1 {
		t.Fatalf("deadline calls = %d, want 1", len(conn.deadlines))
	}
}

func TestWriteTimeoutConnWriteWriteErrorStillClearsDeadline(t *testing.T) {
	wantErr := errors.New("write failed")
	conn := &stubConn{writeN: 2, writeErr: wantErr}
	w := &writeTimeoutConn{Conn: conn, timeout: 50 * time.Millisecond}

	n, err := w.Write([]byte("abc"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write() error = %v, want %v", err, wantErr)
	}
	if n != 2 {
		t.Fatalf("Write() n = %d, want 2", n)
	}
	if len(conn.deadlines) != 2 {
		t.Fatalf("deadline calls = %d, want 2", len(conn.deadlines))
	}
	if !conn.deadlines[1].IsZero() {
		t.Fatal("second deadline should clear to zero")
	}
}
