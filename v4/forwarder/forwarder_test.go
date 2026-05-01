// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package forwarder

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexian000/gosnippets/routines"
)

// newTestGroup returns a Group whose Close and Wait are registered with t.Cleanup.
func newTestGroup(t *testing.T) routines.Group {
	t.Helper()
	g := routines.NewGroup()
	t.Cleanup(func() {
		g.Close()
		_ = g.Wait()
	})
	return g
}

func TestForwarderBidirectional(t *testing.T) {
	g := newTestGroup(t)
	f := New(10, g)

	// accepted ↔ acceptedPeer  (forwarded to/from dialed)
	// dialed   ↔ dialedPeer    (forwarded to/from accepted)
	accepted, acceptedPeer := net.Pipe()
	dialed, dialedPeer := net.Pipe()

	var writeClosedCount atomic.Int32
	var closedCount atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)

	handler := HandlerFuncs{
		WriteClosed: func(_ net.Conn, _ error) {
			writeClosedCount.Add(1)
		},
		Closed: func() {
			closedCount.Add(1)
			wg.Done()
		},
	}

	if err := f.Start(accepted, dialed, handler); err != nil {
		t.Fatal("Start:", err)
	}

	// Data written to acceptedPeer should arrive at dialedPeer.
	want := []byte("bidirectional forwarding test")
	writeErrCh := make(chan error, 1)
	go func() {
		_, err := acceptedPeer.Write(want)
		writeErrCh <- err
	}()

	got := make([]byte, len(want))
	_ = dialedPeer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(dialedPeer, got); err != nil {
		t.Fatal("read from dialedPeer:", err)
	}
	_ = dialedPeer.SetReadDeadline(time.Time{})
	if err := <-writeErrCh; err != nil {
		t.Fatal("write to acceptedPeer:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Close both peer ends to trigger clean shutdown.
	_ = acceptedPeer.Close()
	_ = dialedPeer.Close()

	// Wait for OnClosed to be called (both copy directions finished).
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for forwarder shutdown")
	}

	if hc := writeClosedCount.Load(); hc != 2 {
		t.Fatalf("OnWriteClosed called %d times, want 2", hc)
	}
	if cc := closedCount.Load(); cc != 1 {
		t.Fatalf("OnClosed called %d times, want 1", cc)
	}
}

func TestForwarderNilHandler(t *testing.T) {
	g := newTestGroup(t)
	f := New(10, g)

	accepted, acceptedPeer := net.Pipe()
	dialed, dialedPeer := net.Pipe()

	if err := f.Start(accepted, dialed, nil); err != nil {
		t.Fatal("Start:", err)
	}

	// Verify forwarding works without a handler.
	want := []byte("no handler")
	go func() { _, _ = acceptedPeer.Write(want) }()
	got := make([]byte, len(want))
	_ = dialedPeer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(dialedPeer, got); err != nil {
		t.Fatal("read:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	_ = acceptedPeer.Close()
	_ = dialedPeer.Close()
}

func TestForwarderConnLimit(t *testing.T) {
	g := newTestGroup(t)
	f := New(1, g) // max 1 concurrent connection

	a1, b1 := net.Pipe()
	a2, b2 := net.Pipe()
	t.Cleanup(func() {
		_ = a1.Close()
		_ = b1.Close()
		_ = a2.Close()
		_ = b2.Close()
	})

	if err := f.Start(a1, b1, nil); err != nil {
		t.Fatal("first Start:", err)
	}
	// Counter is now full; a second concurrent Start must be rejected.
	if err := f.Start(a2, b2, nil); err != ErrConnLimit {
		t.Fatalf("second Start: got %v, want ErrConnLimit", err)
	}
}

func TestForwarderCount(t *testing.T) {
	g := newTestGroup(t)
	f := New(10, g)

	if got := f.Count(); got != 0 {
		t.Fatalf("Count() before any starts = %d, want 0", got)
	}

	a1, b1 := net.Pipe()
	a2, b2 := net.Pipe()
	t.Cleanup(func() {
		_ = a1.Close()
		_ = b1.Close()
		_ = a2.Close()
		_ = b2.Close()
	})

	if err := f.Start(a1, b1, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(a2, b2, nil); err != nil {
		t.Fatal(err)
	}
	// Both connections are active; counter holds 2 slots.
	if got := f.Count(); got != 2 {
		t.Fatalf("Count() with 2 active connections = %d, want 2", got)
	}
}

func TestForwarderGroupClosed(t *testing.T) {
	g := routines.NewGroup()
	g.Close() // close before any use

	f := New(10, g)
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	if err := f.Start(a, b, nil); err == nil {
		t.Fatal("Start with closed group: expected error, got nil")
	}
}

func TestForwarderCloseConns(t *testing.T) {
	g := newTestGroup(t)
	f := New(10, g)

	accepted, acceptedPeer := net.Pipe()
	dialed, dialedPeer := net.Pipe()
	t.Cleanup(func() {
		_ = acceptedPeer.Close()
		_ = dialedPeer.Close()
	})

	if err := f.Start(accepted, dialed, nil); err != nil {
		t.Fatal("Start:", err)
	}

	// Close all managed connections; peers should see EOF.
	f.Close()

	_ = acceptedPeer.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	_, err := acceptedPeer.Read(buf)
	if err == nil {
		t.Fatal("expected error reading from peer after Close, got nil")
	}
}
