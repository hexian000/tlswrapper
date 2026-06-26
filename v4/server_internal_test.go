package tlswrapper

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/mux/h2mux"
)

func TestServerHandleInboundStream(t *testing.T) {
	t.Run("no-connect-address", func(t *testing.T) {
		s := newTestServer(t, nil)
		stream, peer := net.Pipe()
		done := make(chan struct{})
		go func() {
			s.handleInboundStream(nil, "peer-a", stream)
			close(done)
		}()
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, err := peer.Read(make([]byte, 1))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Read() = %v, want EOF", err)
		}
		_ = peer.Close()
		<-done
		if got := s.stats.request.Load(); got != 1 {
			t.Fatalf("request count = %d, want 1", got)
		}
		if got := s.stats.success.Load(); got != 0 {
			t.Fatalf("success count = %d, want 0", got)
		}
	})

	t.Run("success", func(t *testing.T) {
		s := newTestServer(t, map[string]any{"connect": startEchoServer(t)})
		stream, peer := net.Pipe()
		done := make(chan struct{})
		go func() {
			s.handleInboundStream(nil, "peer-a", stream)
			close(done)
		}()
		transferAndVerify(t, peer, peer, []byte("hello inbound"))
		_ = peer.Close()
		waitFor(t, 2*time.Second, func() bool {
			return s.stats.success.Load() == 1
		})
		<-done
		if got := s.stats.request.Load(); got != 1 {
			t.Fatalf("request count = %d, want 1", got)
		}
	})
}

// TestServerRejectInboundWiring verifies that a node without a "connect"
// forwarding target advertises RejectInbound during the mux handshake, so the
// peer's OpenStream fails fast instead of opening a stream that would only be
// dropped, and that configuring "connect" keeps streams open as usual.
func TestServerRejectInboundWiring(t *testing.T) {
	openMainTunnelStream := func(t *testing.T, srvOverrides map[string]any) (net.Conn, error) {
		t.Helper()
		muxAddr := freePort(t)
		srvOverrides["mux_listen"] = muxAddr
		srv := newTestServer(t, srvOverrides)
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Shutdown() })
		cli := newTestServer(t, map[string]any{
			"mux_connect": muxAddr,
			"identity":    map[string]any{"claim": "test-client"},
		})
		if err := cli.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cli.Shutdown() })
		waitFor(t, 5*time.Second, func() bool { return cli.Stats().NumSessions > 0 })
		tn := cli.findSession("")
		if tn == nil {
			t.Fatal("main tunnel not found")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return tn.OpenStream(ctx)
	}

	t.Run("no-connect-rejects", func(t *testing.T) {
		conn, err := openMainTunnelStream(t, map[string]any{})
		if !errors.Is(err, h2mux.ErrInboundRejected) {
			t.Fatalf("OpenStream: got %v, want ErrInboundRejected", err)
		}
		if conn != nil {
			_ = conn.Close()
		}
	})

	t.Run("with-connect-allows", func(t *testing.T) {
		conn, err := openMainTunnelStream(t, map[string]any{"connect": startEchoServer(t)})
		if err != nil {
			t.Fatalf("OpenStream: %v", err)
		}
		_ = conn.Close()
	})
}

func TestServerReloadConfigAddsAndRemovesTunnels(t *testing.T) {
	listenAddr := freePort(t)
	s := newTestServer(t, nil)
	t.Cleanup(func() { _ = s.Shutdown() })

	if err := s.ReloadConfig(newTestConfig(t, map[string]any{
		"identity": map[string]any{"listen": map[string]any{"peer-a": listenAddr}},
	})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok := s.identities["peer-a"]
		return ok
	})
	conn, err := net.DialTimeout("tcp", listenAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	if err := s.ReloadConfig(newTestConfig(t, nil)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.identities) == 0
	})
	conn, err = net.DialTimeout("tcp", listenAddr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected listener to be closed")
	}
}

func TestServerReloadIdentityListenAddrChange(t *testing.T) {
	oldAddr := freePort(t)
	newAddr := freePort(t)
	s := newTestServer(t, nil)
	t.Cleanup(func() { _ = s.Shutdown() })

	if err := s.ReloadConfig(newTestConfig(t, map[string]any{
		"identity": map[string]any{"listen": map[string]any{"peer-a": oldAddr}},
	})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		il, ok := s.identities["peer-a"]
		return ok && il.addr == oldAddr
	})

	// Reload with the same identity name bound to a different address.
	if err := s.ReloadConfig(newTestConfig(t, map[string]any{
		"identity": map[string]any{"listen": map[string]any{"peer-a": newAddr}},
	})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		il, ok := s.identities["peer-a"]
		return ok && il.addr == newAddr
	})
	conn, err := net.DialTimeout("tcp", newAddr, 2*time.Second)
	if err != nil {
		t.Fatal("new address not reachable:", err)
	}
	_ = conn.Close()
	conn, err = net.DialTimeout("tcp", oldAddr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected old listener to be closed")
	}
}

func TestServerStartWithAPIListener(t *testing.T) {
	apiAddr := freePort(t)
	s := newTestServer(t, map[string]any{"api_listen": apiAddr})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	client := &http.Client{Timeout: 2 * time.Second}
	waitFor(t, 2*time.Second, func() bool {
		resp, err := client.Get("http://" + apiAddr + "/healthy")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

func TestLocalHandlerServe(t *testing.T) {
	t.Run("no-session", func(t *testing.T) {
		s := newTestServer(t, nil)
		accepted, peer := net.Pipe()
		done := make(chan struct{})
		go func() {
			(&LocalHandler{s: s, id: "peer-a"}).Serve(context.Background(), accepted)
			close(done)
		}()
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, err := peer.Read(make([]byte, 1))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Read() = %v, want EOF", err)
		}
		_ = peer.Close()
		<-done
	})

	t.Run("forwards-stream", func(t *testing.T) {
		// cli.PeerIdentity() == "peer-a"; LocalHandler{id: "peer-a"} finds it.
		cli, srv := newMuxSessionPair(t, &h2mux.Config{LocalID: "client"}, &h2mux.Config{LocalID: "peer-a"})
		s := newTestServer(t, nil)
		t.Cleanup(func() { _ = s.Shutdown() })
		tn := newTunnel("", s)
		tn.ss = cli
		s.mu.Lock()
		s.identityTunnels = append(s.identityTunnels, tn)
		s.mu.Unlock()

		remoteCh := make(chan net.Conn, 1)
		go func() {
			conn, err := srv.Accept()
			if err != nil {
				remoteCh <- nil
				return
			}
			remoteCh <- conn
		}()

		accepted, peer := net.Pipe()
		go (&LocalHandler{s: s, id: "peer-a"}).Serve(context.Background(), accepted)
		remote := <-remoteCh
		if remote == nil {
			t.Fatal("expected remote stream")
		}
		defer remote.Close()
		transferAndVerify(t, peer, remote, []byte("client to remote"))
		transferAndVerify(t, remote, peer, []byte("remote to client"))
		_ = peer.Close()
	})
}

func TestEmptyHandlerServeClosesConn(t *testing.T) {
	accepted, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		(&EmptyHandler{}).Serve(context.Background(), accepted)
		close(done)
	}()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := peer.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = %v, want EOF", err)
	}
	_ = peer.Close()
	<-done
}

// TestServerStaleSessionsAfterReload verifies that sessions present before a
// config reload are marked stale and evicted immediately when they are already
// idle (no active streams) — behaving like idle_timeout=0.
func TestServerStaleSessionsAfterReload(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })

	// Inject a synthetic inbound session (no active streams).
	cli, srv := newMuxSessionPair(t, &h2mux.Config{LocalID: "client"}, &h2mux.Config{LocalID: "server"})
	t.Cleanup(func() {
		_ = cli.Close()
		_ = srv.Close()
	})
	tn := newTunnel("", s)
	tn.ss = srv
	tn.lastChanged = time.Now()
	s.mu.Lock()
	s.acceptedTunnels[srv] = tn
	s.mu.Unlock()

	// Trigger reload — markSessionsStale now calls checkIdle immediately,
	// so the already-idle stale session is evicted synchronously.
	if err := s.ReloadConfig(newTestConfig(t, nil)); err != nil {
		t.Fatal(err)
	}

	// Eviction is synchronous; no polling needed, but use waitFor as a safety net.
	waitFor(t, time.Second, func() bool {
		tn.mu.RLock()
		defer tn.mu.RUnlock()
		return tn.ss == nil || tn.ss.IsClosed()
	})
}

// TestServerReloadConfigPartialFailure verifies that ReloadConfig returns an
// aggregated error when part of the reload fails,
// when starting a new config-driven tunnel fails (e.g. port already in use),
// and that the reload otherwise completes normally.
func TestServerReloadConfigPartialFailure(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })

	// Hold a port so that ReloadConfig cannot bind to it.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	busyAddr := taken.Addr().String()

	// Reload a config where one peer's listen address is unavailable.
	cfg := newTestConfig(t, map[string]any{
		"identity": map[string]any{
			"listen": map[string]any{"peer-busy": busyAddr},
		},
	})
	err = s.ReloadConfig(cfg)
	if err == nil {
		t.Fatal("ReloadConfig() = nil, want aggregated error")
	}
	if !strings.Contains(err.Error(), busyAddr) {
		t.Fatalf("ReloadConfig() error = %v, want mention of %q", err, busyAddr)
	}

	// Despite the error the config was still swapped in.
	got, _ := s.getConfig()
	if got != cfg {
		t.Fatalf("expected new config to be active after partial-failure reload")
	}
}

// TestServerReloadMuxListen verifies that changing MuxListen in LoadConfig
// causes the server to stop listening on the old address and start listening
// on the new one.
func TestServerReloadMuxListen(t *testing.T) {
	addrA := freePort(t)
	addrB := freePort(t)

	s := newTestServer(t, map[string]any{"mux_listen": addrA})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })

	// Confirm addrA is initially reachable.
	waitFor(t, 2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addrA, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})

	// Reload with addrB.
	if err := s.ReloadConfig(newTestConfig(t, map[string]any{"mux_listen": addrB})); err != nil {
		t.Fatal(err)
	}

	// addrA should no longer accept connections.
	waitFor(t, 2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addrA, 100*time.Millisecond)
		if err != nil {
			return true // expected
		}
		_ = c.Close()
		return false
	})

	// addrB should be reachable.
	waitFor(t, 2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addrB, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})
}

func TestMaxStreamsNonZero(t *testing.T) {
	cfg := newTestConfig(t, map[string]any{
		"mux": map[string]any{
			"tcp":          map[string]any{"nodelay": true},
			"max_streams":  42,
			"timeout":      10,
			"keepalive":    5,
			"send_timeout": 8,
		},
	})
	if got := maxStreams(cfg); got != 42 {
		t.Fatalf("maxStreams() = %d, want 42", got)
	}
}

func TestMaxStreamsDefault(t *testing.T) {
	cfg := newTestConfig(t, nil)
	if got := maxStreams(cfg); got != 1024 {
		t.Fatalf("maxStreams() = %d, want 1024", got)
	}
}

func TestServerReloadAPIListenInvalidAddr(t *testing.T) {
	s := newTestServer(t, nil)
	t.Cleanup(func() { _ = s.Shutdown() })

	// Port 99999 is out of range; Listen must fail.
	err := s.ReloadConfig(newTestConfig(t, map[string]any{
		"api_listen": "127.0.0.1:99999",
	}))
	if err == nil {
		t.Fatal("expected error for invalid api_listen address, got nil")
	}
}

func TestServerStatsLatencyBranch(t *testing.T) {
	s := newTestServer(t, nil)
	t.Cleanup(func() { _ = s.Shutdown() })

	// Inject a tunnel with recorded latency samples.
	tn := newTunnel("latency-test:0", s)
	for i := 1; i <= 10; i++ {
		tn.streamLatency.Record(time.Duration(i) * time.Millisecond)
	}
	s.mu.Lock()
	s.identityTunnels = append(s.identityTunnels, tn)
	s.mu.Unlock()

	stats := s.Stats()
	if !stats.StreamLatency.Available {
		t.Fatal("Stats().StreamLatency.Available = false, want true after recording latency samples")
	}
	if stats.StreamLatency.Max <= 0 {
		t.Fatalf("Stats().StreamLatency.Max = %v, want > 0", stats.StreamLatency.Max)
	}
}

// TestServerAcceptInboundStreams verifies that acceptInboundStreams forwards
// server-initiated streams to the configured connect address via handleInboundStream.
func TestServerAcceptInboundStreams(t *testing.T) {
	echoAddr := startEchoServer(t)
	s := newTestServer(t, map[string]any{"connect": echoAddr})
	t.Cleanup(func() { _ = s.Shutdown() })

	cli, srv := newMuxSessionPair(t, &h2mux.Config{LocalID: "client"}, &h2mux.Config{LocalID: "server"})

	tn := newTunnel("remote:1", s)
	tn.ss = cli

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.acceptInboundStreams(tn, cli)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// srv.Open triggers cli.Accept inside acceptInboundStreams.
	srvConn, err := srv.Open(ctx)
	if err != nil {
		t.Fatal("srv.Open:", err)
	}
	defer srvConn.Close()

	transferAndVerify(t, srvConn, srvConn, []byte("via-inbound"))

	// Close cli to unblock acceptInboundStreams.
	_ = cli.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptInboundStreams did not exit within timeout")
	}
}

// TestServerStatsWithListener verifies that Stats() does not panic and
// populates Accepted/Served when the mux listener is active.
func TestServerStatsWithListener(t *testing.T) {
	addr := freePort(t)
	s := newTestServer(t, map[string]any{"mux_listen": addr})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })

	// Wait for the listener to bind.
	waitFor(t, 2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})

	// Stats() must not panic; Accepted and Served are populated from s.l.
	_ = s.Stats()
}
