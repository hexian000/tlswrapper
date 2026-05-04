package tlswrapper

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/mux"
)

func TestTunnelCheckIdle(t *testing.T) {
	t.Run("clears-closed-session", func(t *testing.T) {
		_, srv := newMuxSessionPair(t, &mux.Config{LocalID: "client"}, &mux.Config{LocalID: "server"})
		_ = srv.Close()
		tn := newTunnel("peer-a", "", newTestServer(t, nil))
		tn.ss = srv
		tn.idleSince = time.Now()
		tn.checkIdle()
		if tn.ss != nil {
			t.Fatal("expected closed session to be cleared")
		}
		if !tn.idleSince.IsZero() {
			t.Fatal("expected idleSince to reset")
		}
	})

	t.Run("evicts-idle-session", func(t *testing.T) {
		_, srv := newMuxSessionPair(t, &mux.Config{LocalID: "client"}, &mux.Config{LocalID: "server"})
		tn := newTunnel("peer-a", "", newTestServer(t, map[string]any{"idle_timeout": 5}))
		tn.ss = srv
		tn.idleSince = time.Now().Add(-6 * time.Second)
		tn.checkIdle()
		if tn.ss != nil {
			t.Fatal("expected idle session to be evicted")
		}
		if !srv.IsClosed() {
			t.Fatal("expected session to be closed")
		}
	})
}

func TestTunnelSchedule(t *testing.T) {
	tn := newTunnel("peer-a", "", newTestServer(t, nil))
	if ch := tn.schedule(); ch == nil {
		t.Fatal("expected schedule channel")
	}
	tn.dialAddr = "127.0.0.1:1"
	tn.redialCount = 1
	if ch := tn.schedule(); ch == nil {
		t.Fatal("expected redial schedule channel")
	}
}

func TestTunnelRedialFailureIncrementsCount(t *testing.T) {
	addr := freePort(t)
	tn := newTunnel("peer-a", addr, newTestServer(t, map[string]any{"identity": map[string]any{"claim": "local"}}))
	tn.redial()
	if tn.redialCount != 1 {
		t.Fatalf("redialCount = %d, want 1", tn.redialCount)
	}
}

func TestTunnelRedialSuccessAndDelSession(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serverCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	remoteCh := make(chan *mux.Session, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			remoteCh <- nil
			return
		}
		sess, err := mux.Server(serverCtx, conn, &mux.Config{LocalID: "remote"})
		if err != nil {
			remoteCh <- nil
			return
		}
		remoteCh <- sess
		<-sess.CloseChan()
	}()

	s := newTestServer(t, map[string]any{"identity": map[string]any{"claim": "local"}})
	t.Cleanup(func() { _ = s.Shutdown() })
	tn := newTunnel("peer-a", l.Addr().String(), s)
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
	tn.delSession(ss)
	if tn.getSession() != nil {
		t.Fatal("expected session to be removed")
	}
	select {
	case <-tn.redialSig:
	case <-time.After(2 * time.Second):
		t.Fatal("expected redial signal")
	}
	_ = remote.Close()
}
