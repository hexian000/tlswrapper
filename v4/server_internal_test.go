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

	"github.com/hexian000/tlswrapper/v4/mux"
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
		for _, t := range s.identityTunnels {
			if t.id == "peer-a" {
				return true
			}
		}
		return false
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
		return len(s.identityTunnels) == 0
	})
	conn, err = net.DialTimeout("tcp", listenAddr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected listener to be closed")
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
		cli, srv := newMuxSessionPair(t, &mux.Config{LocalID: "client"}, &mux.Config{LocalID: "peer-a"})
		s := newTestServer(t, nil)
		t.Cleanup(func() { _ = s.Shutdown() })
		tn := newTunnel("", s)
		tn.id = "peer-a"
		tn.ss = srv
		s.mu.Lock()
		s.identityTunnels = append(s.identityTunnels, tn)
		s.mu.Unlock()

		remoteCh := make(chan net.Conn, 1)
		go func() {
			conn, err := cli.Accept()
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
// config reload are marked stale and evicted by maintenanceLoop once they
// become idle (no active streams) — behaving like idle_timeout=0.
func TestServerStaleSessionsAfterReload(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })

	// Inject a synthetic inbound session (no active streams).
	cli, srv := newMuxSessionPair(t, &mux.Config{LocalID: "client"}, &mux.Config{LocalID: "server"})
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

	// Trigger reload — this marks tn as stale.
	if err := s.ReloadConfig(newTestConfig(t, nil)); err != nil {
		t.Fatal(err)
	}

	// maintenanceLoop should evict the stale idle session within ~2 s.
	waitFor(t, 3*time.Second, func() bool {
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
