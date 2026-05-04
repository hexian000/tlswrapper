// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper_test

import (
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	tlswrapper "github.com/hexian000/tlswrapper/v4"
	"github.com/hexian000/tlswrapper/v4/config"
)

// freePort returns an available TCP address on localhost by briefly listening on :0.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startEchoServer starts a simple TCP echo server; the listener is closed on cleanup.
func startEchoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return l.Addr().String()
}

// newPlaintextConfig builds a minimal no-TLS tlswrapper config for testing.
func newPlaintextConfig(t *testing.T, overrides map[string]any) *config.File {
	t.Helper()
	fields := map[string]any{
		"type":         config.Type,
		"timeout":      10,
		"keepalive":    5,
		"send_timeout": 8,
		"max_startups": "10:30:60",
		"mux":          map[string]any{"tcp": map[string]any{"nodelay": true, "backlog": 4}, "max_halfopen": 16},
		"tcp":          map[string]any{"nodelay": true, "backlog": 4},
	}
	for k, v := range overrides {
		fields[k] = v
	}
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestForwardBidirectional verifies end-to-end bidirectional TCP forwarding
// through a plaintext tlswrapper session:
//
//	[test conn] → [client Listen] ──mux──> [server MuxListen] → [echo server]
func TestForwardBidirectional(t *testing.T) {
	// 1. Echo server: reflects all bytes back to the sender.
	echoAddr := startEchoServer(t)

	// 2. Reserve free ports for the mux listener and the client-side listener.
	muxAddr := freePort(t)
	clientListenAddr := freePort(t)

	// 3. Session server: accepts mux connections, forwards streams to the echo server.
	srvCfg := newPlaintextConfig(t, map[string]any{
		"mux_listen": muxAddr,
		"connect":    echoAddr,
	})
	srv, err := tlswrapper.NewServer(srvCfg)
	if err != nil {
		t.Fatal("server create:", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal("server start:", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// 4. Session client: dials the mux server, exposes a local TCP listener.
	// identity.claim is required: the server rejects sessions from an anonymous peer.
	cliCfg := newPlaintextConfig(t, map[string]any{
		"mux_connect": muxAddr,
		"listen":      clientListenAddr,
		"identity":    map[string]any{"claim": "test-client"},
	})
	cli, err := tlswrapper.NewServer(cliCfg)
	if err != nil {
		t.Fatal("client create:", err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal("client start:", err)
	}
	t.Cleanup(func() { _ = cli.Shutdown() })

	// 5. Wait for the outbound mux session to be established (up to 5 s).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cli.Stats().NumSessions > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cli.Stats().NumSessions == 0 {
		t.Fatal("mux session not established within 5 s")
	}

	// 6. Connect through the session and verify bidirectional forwarding.
	conn, err := net.DialTimeout("tcp", clientListenAddr, 3*time.Second)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer conn.Close()

	want := []byte("hello bidirectional forwarding")
	if _, err := conn.Write(want); err != nil {
		t.Fatal("write:", err)
	}

	got := make([]byte, len(want))
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal("read:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}
}

func TestShutdownClosesServiceListeners(t *testing.T) {
	listenAddr := freePort(t)

	cfg := newPlaintextConfig(t, map[string]any{
		"identity": map[string]any{
			"listen": map[string]any{
				"peer-a": listenAddr,
			},
		},
	})
	srv, err := tlswrapper.NewServer(cfg)
	if err != nil {
		t.Fatal("server create:", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal("server start:", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Shutdown()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal("server shutdown:", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server shutdown blocked with service listener active")
	}
}
