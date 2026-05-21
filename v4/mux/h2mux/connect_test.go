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
