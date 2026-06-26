package tlswrapper

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/mux"
	"github.com/hexian000/tlswrapper/v4/mux/h2mux"
)

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

func newTestConfig(t *testing.T, overrides map[string]any) *config.File {
	t.Helper()
	fields := map[string]any{
		"type":         config.Type,
		"max_startups": "10:30:60",
		"log":          "discard",
		"mux": map[string]any{
			"tcp":             map[string]any{"nodelay": true, "backlog": 4},
			"max_halfopen":    16,
			"timeout":         10,
			"keepalive":       5,
			"send_timeout":    8,
			"connect_timeout": 10,
		},
		"tcp": map[string]any{"nodelay": true, "backlog": 4},
	}
	for key, value := range overrides {
		fields[key] = value
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

func newTestServer(t *testing.T, overrides map[string]any) *Server {
	t.Helper()
	s, err := NewServer(newTestConfig(t, overrides))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	})
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not satisfied before timeout")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	oldErr := os.Stderr
	os.Stdout = w
	os.Stderr = w
	defer func() {
		os.Stdout = old
		os.Stderr = oldErr
		_ = r.Close()
	}()
	fn()
	_ = w.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

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
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return l.Addr().String()
}

func newMuxSessionPair(t *testing.T, clientCfg, serverCfg *h2mux.Config) (mux.Session, mux.Session) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	type result struct {
		sess mux.Session
		err  error
	}
	serverCh := make(chan result, 1)
	go func() {
		sess, err := h2mux.Server(ctx, serverConn, serverCfg)
		serverCh <- result{sess: sess, err: err}
	}()

	clientSess, err := h2mux.Client(ctx, clientConn, clientCfg)
	if err != nil {
		t.Fatalf("mux.Client: %v", err)
	}
	serverRes := <-serverCh
	if serverRes.err != nil {
		_ = clientSess.Close()
		t.Fatalf("mux.Server: %v", serverRes.err)
	}
	t.Cleanup(func() {
		_ = clientSess.Close()
		_ = serverRes.sess.Close()
	})
	return clientSess, serverRes.sess
}

func transferAndVerify(t *testing.T, src, dst net.Conn, want []byte) {
	t.Helper()
	writeDone := make(chan error, 1)
	go func() {
		_, err := src.Write(want)
		writeDone <- err
	}()
	got := make([]byte, len(want))
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(dst, got)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transferAndVerify: read timed out")
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
