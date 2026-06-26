// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	. "github.com/hexian000/tlswrapper/v4/mux/h3mux"
)

type acceptResult struct {
	conn net.Conn
	err  error
}

// TestH3StreamHalfClose verifies that streams support a write-side half-close:
// after the opener calls CloseWrite, the acceptor sees EOF but can still send
// data back, which the opener can read.
func TestH3StreamHalfClose(t *testing.T) {
	cli, srv := quicSessions(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srvCh := make(chan acceptResult, 1)
	go func() {
		conn, err := srv.Accept()
		srvCh <- acceptResult{conn, err}
	}()

	cliConn, err := cli.Open(ctx)
	if err != nil {
		t.Fatal("cli.Open:", err)
	}
	defer cliConn.Close()
	res := <-srvCh
	if res.err != nil {
		t.Fatal("srv.Accept:", res.err)
	}
	srvConn := res.conn
	defer srvConn.Close()

	// Client sends request data, then half-closes its write side.
	transferAndVerify(t, cliConn, srvConn, []byte("request"))
	cw, ok := cliConn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("client stream does not implement CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatal("CloseWrite:", err)
	}

	// Server observes EOF on the read side...
	if err := srvConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := srvConn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after peer CloseWrite = %v, want io.EOF", err)
	}
	if err := srvConn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	// ...but can still send the response, which the client reads.
	transferAndVerify(t, srvConn, cliConn, []byte("response"))
}
