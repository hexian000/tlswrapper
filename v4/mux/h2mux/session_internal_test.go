// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"io"
	"testing"
)

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
