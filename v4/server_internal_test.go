package tlswrapper

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
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
			s.handleInboundStream("peer-a", stream)
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
			s.handleInboundStream("peer-a", stream)
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

func TestServerLoadConfigAddsAndRemovesTunnels(t *testing.T) {
	listenAddr := freePort(t)
	s := newTestServer(t, nil)
	t.Cleanup(func() { _ = s.Shutdown() })

	if err := s.LoadConfig(newTestConfig(t, map[string]any{
		"service": map[string]any{"listen": map[string]any{"peer-a": listenAddr}},
	})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok := s.services["peer-a"]
		return ok
	})
	conn, err := net.DialTimeout("tcp", listenAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	if err := s.LoadConfig(newTestConfig(t, nil)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.services) == 0
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

func TestMuxHandlerServe(t *testing.T) {
	t.Run("no-session", func(t *testing.T) {
		s := newTestServer(t, nil)
		accepted, peer := net.Pipe()
		done := make(chan struct{})
		go func() {
			(&MuxHandler{s: s, id: "peer-a"}).Serve(context.Background(), accepted)
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
		tn := newTunnel("peer-a", "", s)
		tn.ss = srv
		s.addSession(tn)

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
		go (&MuxHandler{s: s, id: "peer-a"}).Serve(context.Background(), accepted)
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
