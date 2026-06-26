// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"io"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// closeRecordConn is a minimal net.Conn that records whether Close was called.
type closeRecordConn struct {
	closed atomic.Bool
}

func (c *closeRecordConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *closeRecordConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *closeRecordConn) Close() error                     { c.closed.Store(true); return nil }
func (c *closeRecordConn) LocalAddr() net.Addr              { return h2Addr{"local"} }
func (c *closeRecordConn) RemoteAddr() net.Addr             { return h2Addr{"remote"} }
func (c *closeRecordConn) SetDeadline(time.Time) error      { return nil }
func (c *closeRecordConn) SetReadDeadline(time.Time) error  { return nil }
func (c *closeRecordConn) SetWriteDeadline(time.Time) error { return nil }

// TestDeliverStreamAbandonedRequest verifies that a stream carrying a non-empty
// requestID with no matching pending waiter (the Open() caller already gave up)
// is closed rather than misrouted onto acceptCh as an inbound stream.
func TestDeliverStreamAbandonedRequest(t *testing.T) {
	ctrl := &mockControlStream{recvErr: io.EOF}
	ss := newServerSession(ctrl, nil, nil, nil, "peer", false, nil, nil)
	defer ss.Close()

	conn := &closeRecordConn{}
	// No pending entry registered for "42": simulates an Open() that already
	// timed out and deleted its waiter.
	ss.DeliverStream("42", conn)

	if !conn.closed.Load() {
		t.Fatal("conn was not closed for an abandoned requestID")
	}
	select {
	case <-ss.acceptCh:
		t.Fatal("abandoned request stream was misrouted onto acceptCh")
	default:
	}
}

// countingConn records create/close counts via shared atomics and flags any
// double-close.
type countingConn struct {
	closeCount  *atomic.Int64
	doubleClose *atomic.Int64
	once        atomic.Bool
}

func (c *countingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *countingConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *countingConn) LocalAddr() net.Addr              { return h2Addr{"local"} }
func (c *countingConn) RemoteAddr() net.Addr             { return h2Addr{"remote"} }
func (c *countingConn) SetDeadline(time.Time) error      { return nil }
func (c *countingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *countingConn) SetWriteDeadline(time.Time) error { return nil }
func (c *countingConn) Close() error {
	if c.once.CompareAndSwap(false, true) {
		c.closeCount.Add(1)
	} else {
		c.doubleClose.Add(1)
	}
	return nil
}

// captureControlStream records OpenRequest request_ids written by
// serverSession.Open so the test can drive deliveries; Recv blocks until done.
type captureControlStream struct {
	ridCh chan string
	done  chan struct{}
}

func (c *captureControlStream) Send(msg *muxpb.ControlMessage) error {
	if or := msg.GetOpenRequest(); or != nil {
		c.ridCh <- or.GetRequestId()
	}
	return nil
}

func (c *captureControlStream) Recv() (*muxpb.ControlMessage, error) {
	<-c.done
	return nil, io.EOF
}

// TestServerSessionOpenDeliverConcurrent stresses the Open/DeliverStream handoff
// under heavy interleaving of timeouts and deliveries. It asserts that every
// delivered conn is closed exactly once (no leak, no double-close) regardless of
// who wins the abandon/deliver race. Run with -race to also catch data races.
func TestServerSessionOpenDeliverConcurrent(t *testing.T) {
	const n = 2000
	var created, closed, doubleClose atomic.Int64

	ctrl := &captureControlStream{
		ridCh: make(chan string, n),
		done:  make(chan struct{}),
	}
	ss := newServerSession(ctrl, nil, nil, nil, "peer", false, nil, nil)

	// Deliverer: for every request the server emits, deliver a fresh conn from a
	// dedicated goroutine after a small jitter, so deliveries race concurrently
	// against each Open's timeout/abandon.
	var dwg sync.WaitGroup
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		for rid := range ctrl.ridCh {
			dwg.Add(1)
			go func(rid string) {
				defer dwg.Done()
				time.Sleep(time.Duration(rand.Intn(100)) * time.Microsecond)
				created.Add(1)
				ss.DeliverStream(rid, &countingConn{closeCount: &closed, doubleClose: &doubleClose})
			}(rid)
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(),
				time.Duration(rand.Intn(200))*time.Microsecond)
			defer cancel()
			conn, err := ss.Open(ctx)
			if err == nil {
				// Caller owns a successfully opened conn.
				_ = conn.Close()
			}
		}()
	}
	wg.Wait()

	// No more Open calls => no more requests; wait for all deliveries (including
	// late ones that close abandoned conns) to complete.
	close(ctrl.ridCh)
	<-scanDone
	dwg.Wait()

	if c, k := created.Load(), closed.Load(); c != k {
		t.Fatalf("conn leak: created=%d closed=%d", c, k)
	}
	if d := doubleClose.Load(); d != 0 {
		t.Fatalf("double close detected: %d", d)
	}

	ss.Close()
	close(ctrl.done) // unblock recvControlLoop so its goroutine exits
}

// TestClientSessionNilAddrs verifies that newClientSession substitutes h2Addr
// fallbacks for LocalAddr and RemoteAddr when both are passed as nil.
func TestClientSessionNilAddrs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// mockControlStream immediately returns EOF so the background goroutine exits cleanly.
	ctrl := &mockControlStream{recvErr: io.EOF}

	ss := newClientSession(
		ctrl,
		nil, // grpcClient – not invoked in this test
		ctx,
		cancel,
		nil, // cleanup
		nil, // localAddr → should become h2Addr{"local"}
		nil, // remoteAddr → should become h2Addr{"remote"}
		"peer",
		false,
		nil, // metrics
		nil, // idleNotify
	)
	defer ss.Close()

	if got := ss.LocalAddr().String(); got != "local" {
		t.Fatalf("LocalAddr().String() = %q, want %q", got, "local")
	}
	if got := ss.RemoteAddr().String(); got != "remote" {
		t.Fatalf("RemoteAddr().String() = %q, want %q", got, "remote")
	}
}

// TestServerSessionNilAddrs verifies that newServerSession substitutes h2Addr
// fallbacks for LocalAddr and RemoteAddr when both are passed as nil.
func TestServerSessionNilAddrs(t *testing.T) {
	ctrl := &mockControlStream{recvErr: io.EOF}

	ss := newServerSession(
		ctrl,
		nil, // cleanup
		nil, // localAddr → should become h2Addr{"local"}
		nil, // remoteAddr → should become h2Addr{"remote"}
		"peer",
		false,
		nil, // metrics
		nil, // idleNotify
	)
	defer ss.Close()

	if got := ss.LocalAddr().String(); got != "local" {
		t.Fatalf("LocalAddr().String() = %q, want %q", got, "local")
	}
	if got := ss.RemoteAddr().String(); got != "remote" {
		t.Fatalf("RemoteAddr().String() = %q, want %q", got, "remote")
	}
}
