// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	mux "github.com/hexian000/tlswrapper/v4/mux"
	. "github.com/hexian000/tlswrapper/v4/mux/h3mux"
)

// generateSelfSignedTLS creates a minimal self-signed ECDSA certificate pair
// for use in in-process tests. The returned *tls.Config has skip-verify set
// on the client side for simplicity.
func generateSelfSignedTLS(t *testing.T) (serverTLS, clientTLS *tls.Config) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "h3mux-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}

	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair: %v", err)
	}

	serverTLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	clientTLS = &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test-only
		MinVersion:         tls.VersionTLS13,
	}
	return serverTLS, clientTLS
}

// quicSessions creates a pair of h3mux sessions over a real QUIC loopback
// connection.  Both sessions are closed via t.Cleanup when the test ends.
func quicSessions(t *testing.T, clientCfg, serverCfg *Config) (cli, srv mux.Session) {
	t.Helper()
	serverTLS, clientTLS := generateSelfSignedTLS(t)

	serverCfg.TLSConfig = serverTLS
	clientCfg.TLSConfig = clientTLS

	listener, err := Listen("127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	addr := listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	type result struct {
		sess mux.Session
		err  error
	}
	srvCh := make(chan result, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		if err != nil {
			srvCh <- result{nil, err}
			return
		}
		sess, err := NewSession(ctx, conn, serverCfg)
		srvCh <- result{sess, err}
	}()

	cliSess, err := Dial(ctx, addr, clientCfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	res := <-srvCh
	if res.err != nil {
		_ = cliSess.Close()
		t.Fatalf("NewSession: %v", res.err)
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
	cli, srv := quicSessions(t,
		&Config{LocalID: "client-id"},
		&Config{LocalID: "server-id"},
	)
	if got := cli.PeerIdentity(); got != "server-id" {
		t.Fatalf("cli.PeerIdentity() = %q, want %q", got, "server-id")
	}
	if got := srv.PeerIdentity(); got != "client-id" {
		t.Fatalf("srv.PeerIdentity() = %q, want %q", got, "client-id")
	}
}

func TestSessionClientOpen(t *testing.T) {
	cli, srv := quicSessions(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
	ctx := context.Background()

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
	transferAndVerify(t, res.conn, cliConn, []byte("reply from server"))
}

func TestSessionServerOpen(t *testing.T) {
	cli, srv := quicSessions(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := cli.Accept()
		acceptCh <- acceptResult{conn, err}
	}()

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
	cli, srv := quicSessions(t, &Config{}, &Config{})

	_ = cli.Close()
	_ = srv.Close()

	ctx := context.Background()
	_, err := cli.Open(ctx)
	if !errors.Is(err, mux.ErrSessionClosed) {
		t.Fatalf("cli.Open after close: got %v, want ErrSessionClosed", err)
	}

	_, err = srv.Accept()
	if !errors.Is(err, mux.ErrSessionClosed) {
		t.Fatalf("srv.Accept after close: got %v, want ErrSessionClosed", err)
	}
}

func TestSessionRejectInbound(t *testing.T) {
	// serverCfg.RejectInbound=true: server tells the client "don't open streams to me".
	cli, _ := quicSessions(t,
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
	cli, srv := quicSessions(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
	ctx := context.Background()

	const n = 5
	type pair struct{ cli, srv net.Conn }
	pairs := make([]pair, n)

	for i := range n {
		acceptCh := make(chan net.Conn, 1)
		go func() {
			conn, err := srv.Accept()
			if err != nil {
				t.Errorf("srv.Accept [%d]: %v", i, err)
				acceptCh <- nil
				return
			}
			acceptCh <- conn
		}()
		cliConn, err := cli.Open(ctx)
		if err != nil {
			t.Fatalf("cli.Open [%d]: %v", i, err)
		}
		srvConn := <-acceptCh
		if srvConn == nil {
			t.FailNow()
		}
		pairs[i] = pair{cliConn, srvConn}
	}

	for i, p := range pairs {
		want := []byte("stream data for pair " + string(rune('0'+i)))
		transferAndVerify(t, p.cli, p.srv, want)
		_ = p.cli.Close()
		_ = p.srv.Close()
	}
}

// TestH3MuxWrapperDialAndListen exercises the H3Mux and H3Listener wrapper
// types end-to-end using real UDP: New/Dial, ListenMux/NewListener/AcceptSession/Addr/Close.
func TestH3MuxWrapperDialAndListen(t *testing.T) {
	serverTLS, clientTLS := generateSelfSignedTLS(t)

	serverCfg := &Config{TLSConfig: serverTLS}
	clientCfg := &Config{TLSConfig: clientTLS}

	ml, err := ListenMux("127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatal("ListenMux:", err)
	}
	t.Cleanup(func() { _ = ml.Close() })

	if ml.Addr() == nil {
		t.Fatal("ml.Addr() = nil, want non-nil")
	}

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

	h := New(clientCfg)
	cliSess, err := h.Dial(ctx, ml.Addr().String())
	if err != nil {
		t.Fatal("H3Mux.Dial:", err)
	}
	t.Cleanup(func() { _ = cliSess.Close() })

	res := <-srvCh
	if res.err != nil {
		t.Fatal("H3Listener.AcceptSession:", res.err)
	}
	t.Cleanup(func() { _ = res.sess.Close() })

	if got := cliSess.PeerIdentity(); got != "" {
		t.Logf("cli.PeerIdentity() = %q", got)
	}
}

// TestH3MuxNewSession exercises H3Mux.NewSession by obtaining a raw *quic.Conn
// from the quic package and upgrading it through the wrapper.
func TestH3MuxNewSession(t *testing.T) {
	serverTLS, clientTLS := generateSelfSignedTLS(t)

	serverCfg := &Config{TLSConfig: serverTLS, LocalID: "srv"}
	clientCfg := &Config{TLSConfig: clientTLS, LocalID: "cli"}

	l, err := Listen("127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatal("Listen:", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	type srvResult struct {
		sess mux.Session
		err  error
	}
	srvCh := make(chan srvResult, 1)
	go func() {
		conn, err := l.Accept(ctx)
		if err != nil {
			srvCh <- srvResult{nil, err}
			return
		}
		h := New(serverCfg)
		sess, err := h.NewSession(ctx, conn)
		srvCh <- srvResult{sess, err}
	}()

	cliSess, err := Dial(ctx, l.Addr().String(), clientCfg)
	if err != nil {
		t.Fatal("Dial:", err)
	}
	t.Cleanup(func() { _ = cliSess.Close() })

	res := <-srvCh
	if res.err != nil {
		t.Fatal("H3Mux.NewSession:", res.err)
	}
	t.Cleanup(func() { _ = res.sess.Close() })

	if got := cliSess.PeerIdentity(); got != "srv" {
		t.Fatalf("cli.PeerIdentity() = %q, want %q", got, "srv")
	}
	if got := res.sess.PeerIdentity(); got != "cli" {
		t.Fatalf("srv.PeerIdentity() = %q, want %q", got, "cli")
	}
}

// TestH3ListenerClose verifies that Close() causes a blocking AcceptSession to return an error.
func TestH3ListenerClose(t *testing.T) {
	serverTLS, _ := generateSelfSignedTLS(t)
	ml, err := ListenMux("127.0.0.1:0", &Config{TLSConfig: serverTLS})
	if err != nil {
		t.Fatal("ListenMux:", err)
	}

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
			t.Fatal("AcceptSession after Close() returned nil error, want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AcceptSession did not return after Close()")
	}
}

// TestH3DialError verifies that Dial returns an error when no server is reachable.
func TestH3DialError(t *testing.T) {
	_, clientTLS := generateSelfSignedTLS(t)
	cfg := &Config{TLSConfig: clientTLS, LocalID: "cli"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Port 1 is almost always unreachable; QUIC will fail to establish a connection.
	_, err := Dial(ctx, "127.0.0.1:1", cfg)
	if err == nil {
		t.Fatal("Dial to port 1: expected error, got nil")
	}
}

// TestH3ListenMuxError verifies that ListenMux returns an error for an invalid address.
func TestH3ListenMuxError(t *testing.T) {
	serverTLS, _ := generateSelfSignedTLS(t)
	_, err := ListenMux("invalid-addr:invalid-port", &Config{TLSConfig: serverTLS})
	if err == nil {
		t.Fatal("ListenMux with invalid addr: expected error, got nil")
	}
}

// TestH3SessionAccessors verifies LocalAddr, RemoteAddr, Stats, CloseChan, and IdleChan
// on both sides of a live h3mux session pair.
func TestH3SessionAccessors(t *testing.T) {
	cli, srv := quicSessions(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})

	for _, name := range []string{"cli", "srv"} {
		s := cli
		if name == "srv" {
			s = srv
		}
		if s.LocalAddr() == nil {
			t.Errorf("%s.LocalAddr() = nil", name)
		}
		if s.RemoteAddr() == nil {
			t.Errorf("%s.RemoteAddr() = nil", name)
		}
		if s.Stats() == nil {
			t.Errorf("%s.Stats() = nil", name)
		}
		if s.CloseChan() == nil {
			t.Errorf("%s.CloseChan() = nil", name)
		}
		if s.IdleChan() == nil {
			t.Errorf("%s.IdleChan() = nil", name)
		}
	}

	// Open one stream, close it, and verify IdleChan fires when NumStreams drops to 0.
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
		// NumStreams reached 0; idle signal received.
	case <-time.After(5 * time.Second):
		t.Fatal("cli.IdleChan() did not fire after closing last stream")
	}
}

// TestH3StreamLocalRemoteAddr verifies that streams returned by Open/Accept
// expose LocalAddr and RemoteAddr.
func TestH3StreamLocalRemoteAddr(t *testing.T) {
	cli, srv := quicSessions(t, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
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
	defer cliConn.Close()

	srvConn := <-acceptCh
	if srvConn == nil {
		t.Fatal("srv.Accept returned nil")
	}
	defer srvConn.Close()

	if cliConn.LocalAddr() == nil {
		t.Error("cliConn.LocalAddr() = nil")
	}
	if cliConn.RemoteAddr() == nil {
		t.Error("cliConn.RemoteAddr() = nil")
	}
	if srvConn.LocalAddr() == nil {
		t.Error("srvConn.LocalAddr() = nil")
	}
	if srvConn.RemoteAddr() == nil {
		t.Error("srvConn.RemoteAddr() = nil")
	}
}
