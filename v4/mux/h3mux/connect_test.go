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
