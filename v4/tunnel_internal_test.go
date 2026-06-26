// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/mux"
	"github.com/hexian000/tlswrapper/v4/mux/h2mux"
)

func TestTunnelCheckIdle(t *testing.T) {
	t.Run("ignores-closed-session", func(t *testing.T) {
		_, srv := newMuxSessionPair(t, &h2mux.Config{LocalID: "client"}, &h2mux.Config{LocalID: "server"})
		_ = srv.Close()
		tn := newTunnel("", newTestServer(t, nil))
		tn.ss = srv
		tn.idleSince = time.Now()
		// Closed sessions are finalized by their watcher (outbound) or
		// serveSession (inbound); checkIdle must leave them alone.
		tn.checkIdle()
		if tn.getSession() != nil {
			t.Fatal("expected no active session")
		}
	})

	t.Run("evicts-idle-session", func(t *testing.T) {
		_, srv := newMuxSessionPair(t, &h2mux.Config{LocalID: "client"}, &h2mux.Config{LocalID: "server"})
		tn := newTunnel("", newTestServer(t, map[string]any{"mux": map[string]any{"idle_timeout": 10}}))
		tn.ss = srv
		tn.idleSince = time.Now().Add(-11 * time.Second)
		tn.checkIdle()
		if !srv.IsClosed() {
			t.Fatal("expected session to be closed")
		}
		if tn.getSession() != nil {
			t.Fatal("expected no active session after eviction")
		}
		if !tn.idleEvicted {
			t.Fatal("expected idleEvicted to be set to suppress auto-redial")
		}
	})
}

// startMuxAcceptor runs an h2mux server that accepts any number of sessions
// (and drains their inbound streams) until the listener closes.
func startMuxAcceptor(t *testing.T) string {
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
			go func(c net.Conn) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				sess, err := h2mux.Server(ctx, c, &h2mux.Config{LocalID: "remote"})
				if err != nil {
					_ = c.Close()
					return
				}
				for {
					stream, err := sess.Accept()
					if err != nil {
						return
					}
					_ = stream.Close()
				}
			}(conn)
		}
	}()
	return l.Addr().String()
}

// TestTunnelIdleEvictionAndDialOnDemand covers the full eviction lifecycle:
// session counters must balance after an idle eviction, the eviction must not
// schedule an automatic redial, and OpenStream must dial on demand afterwards.
func TestTunnelIdleEvictionAndDialOnDemand(t *testing.T) {
	addr := startMuxAcceptor(t)
	s := newTestServer(t, map[string]any{
		"identity": map[string]any{"claim": "local"},
		"mux": map[string]any{
			"tcp":             map[string]any{"nodelay": true, "backlog": 4},
			"max_halfopen":    16,
			"timeout":         10,
			"keepalive":       5,
			"send_timeout":    8,
			"connect_timeout": 10,
			"idle_timeout":    10,
		},
	})
	t.Cleanup(func() { _ = s.Shutdown() })
	tn := newTunnel(addr, s)

	tn.redial()
	if tn.getSession() == nil {
		t.Fatal("expected active session after redial")
	}
	if got := s.stats.numSessions.Load(); got != 1 {
		t.Fatalf("numSessions = %d, want 1", got)
	}

	// Simulate the idle period having elapsed, then evict.
	tn.mu.Lock()
	tn.idleSince = time.Now().Add(-11 * time.Second)
	tn.mu.Unlock()
	tn.checkIdle()
	waitFor(t, 2*time.Second, func() bool {
		return tn.getSession() == nil &&
			s.stats.numSessionsFinalized.Load() == 1 &&
			s.stats.numSessions.Load() == 0
	})
	// An intentional idle eviction must not schedule an automatic redial.
	select {
	case <-tn.redialSig:
		t.Fatal("unexpected redial signal after idle eviction")
	default:
	}

	// OpenStream reconnects on demand.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tn.OpenStream(ctx)
	if err != nil {
		t.Fatal("OpenStream:", err)
	}
	_ = conn.Close()
	if got := s.stats.numSessions.Load(); got != 1 {
		t.Fatalf("numSessions after dial-on-demand = %d, want 1", got)
	}
	if got := s.stats.numSessionsCreated.Load(); got != 2 {
		t.Fatalf("numSessionsCreated = %d, want 2", got)
	}
}

func TestTunnelSchedule(t *testing.T) {
	// Connected (no failures): schedule returns nil so run() idles on events only.
	tn := newTunnel("127.0.0.1:1", newTestServer(t, nil))
	if ch := tn.schedule(); ch != nil {
		t.Fatal("expected nil schedule channel when connected")
	}
	// After a redial failure the backoff table kicks in.
	tn.redialCount = 1
	if ch := tn.schedule(); ch == nil {
		t.Fatal("expected redial schedule channel after failure")
	}
}

func TestTunnelRedialFailureIncrementsCount(t *testing.T) {
	addr := freePort(t)
	tn := newTunnel(addr, newTestServer(t, map[string]any{"identity": map[string]any{"claim": "local"}}))
	tn.redial()
	if tn.redialCount != 1 {
		t.Fatalf("redialCount = %d, want 1", tn.redialCount)
	}
}

func TestTunnelRedialSuccessAndFinalizeSession(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serverCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	remoteCh := make(chan mux.Session, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			remoteCh <- nil
			return
		}
		sess, err := h2mux.Server(serverCtx, conn, &h2mux.Config{LocalID: "remote"})
		if err != nil {
			remoteCh <- nil
			return
		}
		remoteCh <- sess
		<-sess.CloseChan()
	}()

	s := newTestServer(t, map[string]any{"identity": map[string]any{"claim": "local"}})
	t.Cleanup(func() { _ = s.Shutdown() })
	tn := newTunnel(l.Addr().String(), s)
	tn.redialCount = 2
	tn.redial()
	if tn.redialCount != 0 {
		t.Fatalf("redialCount = %d, want 0", tn.redialCount)
	}
	ss := tn.getSession()
	if ss == nil {
		t.Fatal("expected active session after redial")
	}
	remote := <-remoteCh
	if remote == nil {
		t.Fatal("expected remote session")
	}
	// Closing the session triggers the watcher registered by dial, which runs
	// finalizeSession: state cleared, redial signalled.
	created := s.stats.numSessionsCreated.Load()
	finalized := s.stats.numSessionsFinalized.Load()
	if created != finalized+1 {
		t.Fatalf("numSessionsCreated = %d, want %d", created, finalized+1)
	}
	_ = ss.Close()
	waitFor(t, 2*time.Second, func() bool {
		return tn.getSession() == nil &&
			s.stats.numSessionsFinalized.Load() == finalized+1
	})
	select {
	case <-tn.redialSig:
	case <-time.After(2 * time.Second):
		t.Fatal("expected redial signal")
	}
	_ = remote.Close()
}
