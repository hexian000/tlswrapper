// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	mux "github.com/hexian000/tlswrapper/v4/mux"
)

// pipeSession creates a pair of connected mux Sessions over an in-memory net.Pipe().
// Both sessions are closed via t.Cleanup when the test ends.
func pipeSession(t *testing.T, clientCfg, serverCfg *Config) (cli, srv mux.Session) {
	t.Helper()
	clientConn, serverConn := net.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	type result struct {
		sess mux.Session
		err  error
	}
	srvCh := make(chan result, 1)
	go func() {
		sess, err := Server(ctx, serverConn, serverCfg)
		srvCh <- result{sess, err}
	}()

	cliSess, err := Client(ctx, clientConn, clientCfg)
	if err != nil {
		t.Fatalf("mux.Client: %v", err)
	}

	res := <-srvCh
	if res.err != nil {
		_ = cliSess.Close()
		t.Fatalf("mux.Server: %v", res.err)
	}

	t.Cleanup(func() {
		_ = cliSess.Close()
		_ = res.sess.Close()
	})
	return cliSess, res.sess
}

// transferAndVerify writes want to src and reads it from dst, verifying the content.
func transferAndVerify(t *testing.T, src, dst net.Conn, want []byte) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		_, err := src.Write(want)
		errCh <- err
	}()
	got := make([]byte, len(want))
	if err := dst.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, mux.ErrNoDeadline) {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(dst, got); err != nil {
		t.Fatal("read:", err)
	}
	if err := dst.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, mux.ErrNoDeadline) {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal("write:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("data mismatch: got %q, want %q", got, want)
	}
}

func TestSessionPeerIdentity(t *testing.T) {
	cli, srv := pipeSession(t, &Config{LocalID: "client-id"}, &Config{LocalID: "server-id"})

	if got := cli.PeerIdentity(); got != "server-id" {
		t.Fatalf("cli.PeerIdentity() = %q, want %q", got, "server-id")
	}
	if got := srv.PeerIdentity(); got != "client-id" {
		t.Fatalf("srv.PeerIdentity() = %q, want %q", got, "client-id")
	}
}

func TestSessionClientOpen(t *testing.T) {
	cli, srv := pipeSession(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
	ctx := context.Background()

	// Client opens a stream; server accepts it.
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := srv.Accept()
		acceptCh <- acceptResult{conn, err}
	}()

	cliConn, err := cli.Open(ctx)
	if err != nil {
		t.Fatal("cli.Open:", err)
	}
	defer cliConn.Close()

	res := <-acceptCh
	if res.err != nil {
		t.Fatal("srv.Accept:", res.err)
	}
	defer res.conn.Close()

	transferAndVerify(t, cliConn, res.conn, []byte("hello from client"))
	transferAndVerify(t, res.conn, cliConn, []byte("hello from server"))
}

func TestSessionServerOpen(t *testing.T) {
	cli, srv := pipeSession(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Client runs Accept in a goroutine waiting for the server-initiated stream.
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := cli.Accept()
		acceptCh <- acceptResult{conn, err}
	}()

	// Server opens a stream to the client.
	srvConn, err := srv.Open(ctx)
	if err != nil {
		t.Fatal("srv.Open:", err)
	}
	defer srvConn.Close()

	res := <-acceptCh
	if res.err != nil {
		t.Fatal("cli.Accept:", res.err)
	}
	defer res.conn.Close()

	transferAndVerify(t, srvConn, res.conn, []byte("server-to-client"))
	transferAndVerify(t, res.conn, srvConn, []byte("client-to-server"))
}

func TestSessionClose(t *testing.T) {
	cli, srv := pipeSession(t, &Config{}, &Config{})

	_ = cli.Close()
	_ = srv.Close()

	// Subsequent Open should return ErrSessionClosed immediately.
	ctx := context.Background()
	_, err := cli.Open(ctx)
	if !errors.Is(err, mux.ErrSessionClosed) {
		t.Fatalf("cli.Open after close: got %v, want ErrSessionClosed", err)
	}

	// Subsequent Accept should return ErrSessionClosed immediately.
	_, err = srv.Accept()
	if !errors.Is(err, mux.ErrSessionClosed) {
		t.Fatalf("srv.Accept after close: got %v, want ErrSessionClosed", err)
	}
}

func TestSessionRejectInbound(t *testing.T) {
	// serverCfg.RejectInbound=true: server tells the client "don't open streams to me".
	// This is stored in cliSess.peerRejectsInbound, so cli.Open() should fail.
	cli, _ := pipeSession(t,
		&Config{LocalID: "cli"},
		&Config{LocalID: "srv", RejectInbound: true},
	)

	ctx := context.Background()
	_, err := cli.Open(ctx)
	if !errors.Is(err, ErrInboundRejected) {
		t.Fatalf("cli.Open: got %v, want ErrInboundRejected", err)
	}
}

func TestSessionMultipleStreams(t *testing.T) {
	cli, srv := pipeSession(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
	ctx := context.Background()

	const n = 5
	type pair struct{ cli, srv net.Conn }
	pairs := make([]pair, n)

	for i := range n {
		acceptCh := make(chan net.Conn, 1)
		go func() {
			conn, err := srv.Accept()
			if err != nil {
				t.Errorf("srv.Accept[%d]: %v", i, err)
				acceptCh <- nil
				return
			}
			acceptCh <- conn
		}()

		cliConn, err := cli.Open(ctx)
		if err != nil {
			t.Fatalf("cli.Open[%d]: %v", i, err)
		}
		srvConn := <-acceptCh
		if srvConn == nil {
			t.Fatalf("srv.Accept[%d] returned nil", i)
		}
		pairs[i] = pair{cliConn, srvConn}
	}

	// Verify all streams are independently usable.
	for i, p := range pairs {
		msg := []byte{byte(i), byte(i + 1), byte(i + 2)}
		transferAndVerify(t, p.cli, p.srv, msg)
	}

	for _, p := range pairs {
		_ = p.cli.Close()
		_ = p.srv.Close()
	}
}

// TestH2MuxDialAndListener exercises New/Dial and NewListener/AcceptSession over real TCP.
func TestH2MuxDialAndListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	ml := NewListener(l, &Config{LocalID: "server"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	type srvResult struct {
		sess mux.Session
		err  error
	}
	srvCh := make(chan srvResult, 1)
	go func() {
		sess, err := ml.AcceptSession(ctx)
		srvCh <- srvResult{sess, err}
	}()

	dialer := New(&Config{LocalID: "client"})
	cliSess, err := dialer.Dial(ctx, l.Addr().String())
	if err != nil {
		t.Fatal("Dial:", err)
	}
	t.Cleanup(func() { _ = cliSess.Close() })

	res := <-srvCh
	if res.err != nil {
		_ = cliSess.Close()
		t.Fatal("AcceptSession:", res.err)
	}
	t.Cleanup(func() { _ = res.sess.Close() })

	if got := cliSess.PeerIdentity(); got != "server" {
		t.Fatalf("cli.PeerIdentity() = %q, want %q", got, "server")
	}
	if got := res.sess.PeerIdentity(); got != "client" {
		t.Fatalf("srv.PeerIdentity() = %q, want %q", got, "client")
	}
	if ml.Addr() == nil {
		t.Fatal("ml.Addr() = nil, want non-nil")
	}
}

// TestH2ListenerClose verifies that Close() causes a blocking AcceptSession to return.
func TestH2ListenerClose(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ml := NewListener(l, &Config{LocalID: "server"})
	ctx := context.Background()

	errCh := make(chan error, 1)
	go func() {
		_, err := ml.AcceptSession(ctx)
		errCh <- err
	}()

	_ = ml.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("AcceptSession: expected error after Close(), got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AcceptSession did not return after Close()")
	}
}

// TestSessionAccessors verifies LocalAddr, RemoteAddr, Stats, and IdleChan on both sessions.
func TestSessionAccessors(t *testing.T) {
	cli, srv := pipeSession(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})

	if cli.LocalAddr() == nil {
		t.Fatal("cli.LocalAddr() = nil")
	}
	if cli.RemoteAddr() == nil {
		t.Fatal("cli.RemoteAddr() = nil")
	}
	if srv.LocalAddr() == nil {
		t.Fatal("srv.LocalAddr() = nil")
	}
	if srv.RemoteAddr() == nil {
		t.Fatal("srv.RemoteAddr() = nil")
	}
	if cli.Stats() == nil {
		t.Fatal("cli.Stats() = nil")
	}
	if srv.Stats() == nil {
		t.Fatal("srv.Stats() = nil")
	}

	// Open one stream and close it; IdleChan should fire when NumStreams → 0.
	ctx := context.Background()
	acceptCh := make(chan net.Conn, 1)
	go func() {
		conn, err := srv.Accept()
		if err != nil {
			acceptCh <- nil
			return
		}
		acceptCh <- conn
	}()

	cliConn, err := cli.Open(ctx)
	if err != nil {
		t.Fatal("cli.Open:", err)
	}

	srvConn := <-acceptCh
	if srvConn == nil {
		t.Fatal("srv.Accept returned nil")
	}

	_ = cliConn.Close()
	_ = srvConn.Close()

	select {
	case <-cli.IdleChan():
		// NumStreams dropped to 0; idle signal received.
	case <-time.After(5 * time.Second):
		t.Fatal("IdleChan did not fire within timeout after closing last stream")
	}
}

// TestH2MuxDialWithConnSetup verifies that the ConnSetup callback in Config is
// invoked on the dialed connection before the mux handshake.
func TestH2MuxDialWithConnSetup(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	serverCfg := &Config{LocalID: "server"}
	ml := NewListener(l, serverCfg)
	t.Cleanup(func() { _ = ml.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	type srvResult struct {
		sess mux.Session
		err  error
	}
	srvCh := make(chan srvResult, 1)
	go func() {
		sess, err := ml.AcceptSession(ctx)
		srvCh <- srvResult{sess, err}
	}()

	setupCalled := false
	clientCfg := &Config{
		LocalID: "client",
		ConnSetup: func(_ net.Conn) {
			setupCalled = true
		},
	}
	cliSess, err := New(clientCfg).Dial(ctx, l.Addr().String())
	if err != nil {
		t.Fatal("Dial:", err)
	}
	t.Cleanup(func() { _ = cliSess.Close() })

	res := <-srvCh
	if res.err != nil {
		t.Fatal("AcceptSession:", res.err)
	}
	t.Cleanup(func() { _ = res.sess.Close() })

	if !setupCalled {
		t.Fatal("ConnSetup was not called")
	}
}
