// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	tlswrapper "github.com/hexian000/tlswrapper/v4"
	"github.com/hexian000/tlswrapper/v4/config"
)

// freePort returns an available TCP address on localhost by briefly listening on :0.
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

// waitFor polls cond every 10 ms until it returns true or timeout elapses.
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

// startEchoServer starts a simple TCP echo server; the listener is closed on cleanup.
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
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return l.Addr().String()
}

// newPlaintextConfig builds a minimal no-TLS tlswrapper config for testing.
func newPlaintextConfig(t *testing.T, overrides map[string]any) *config.File {
	t.Helper()
	fields := map[string]any{
		"type":         config.Type,
		"max_startups": "10:30:60",
		"mux":          map[string]any{"tcp": map[string]any{"nodelay": true, "backlog": 4}, "max_halfopen": 16, "timeout": 10, "keepalive": 5, "send_timeout": 8, "connect_timeout": 10},
		"tcp":          map[string]any{"nodelay": true, "backlog": 4},
	}
	for k, v := range overrides {
		fields[k] = v
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

// TestForwardBidirectional verifies end-to-end bidirectional TCP forwarding
// through a plaintext tlswrapper session:
//
//	[test conn] → [client Listen] ──mux──> [server MuxListen] → [echo server]
func TestForwardBidirectional(t *testing.T) {
	// 1. Echo server: reflects all bytes back to the sender.
	echoAddr := startEchoServer(t)

	// 2. Reserve free ports for the mux listener and the client-side listener.
	muxAddr := freePort(t)
	clientListenAddr := freePort(t)

	// 3. Session server: accepts mux connections, forwards streams to the echo server.
	srvCfg := newPlaintextConfig(t, map[string]any{
		"mux_listen": muxAddr,
		"connect":    echoAddr,
	})
	srv, err := tlswrapper.NewServer(srvCfg)
	if err != nil {
		t.Fatal("server create:", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal("server start:", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// 4. Session client: dials the mux server, exposes a local TCP listener.
	// identity.claim is required: the server rejects sessions from an anonymous peer.
	cliCfg := newPlaintextConfig(t, map[string]any{
		"mux_connect": muxAddr,
		"listen":      clientListenAddr,
		"identity":    map[string]any{"claim": "test-client"},
	})
	cli, err := tlswrapper.NewServer(cliCfg)
	if err != nil {
		t.Fatal("client create:", err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal("client start:", err)
	}
	t.Cleanup(func() { _ = cli.Shutdown() })

	// 5. Wait for the outbound mux session to be established (up to 5 s).
	waitFor(t, 5*time.Second, func() bool { return cli.Stats().NumSessions > 0 })

	// 6. Connect through the session and verify bidirectional forwarding.
	conn, err := net.DialTimeout("tcp", clientListenAddr, 3*time.Second)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer conn.Close()

	want := []byte("hello bidirectional forwarding")
	if _, err := conn.Write(want); err != nil {
		t.Fatal("write:", err)
	}

	got := make([]byte, len(want))
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal("read:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}
}

// TestForwardIdentityListenMuxConnect exercises the identity.mux_connect +
// identity.listen config pattern, where the outbound dial address and the
// per-peer application listener are declared inside the identity block:
//
//	[test conn] → [client identity.listen["server"]] ──mux──> [server mux_listen] → [echo server]
func TestForwardIdentityListenMuxConnect(t *testing.T) {
	echoAddr := startEchoServer(t)
	muxAddr := freePort(t)
	clientListenAddr := freePort(t)

	// Server: accepts mux connections, forwards streams to the echo server.
	srvCfg := newPlaintextConfig(t, map[string]any{
		"mux_listen": muxAddr,
		"connect":    echoAddr,
		"identity":   map[string]any{"claim": "server"},
	})
	srv, err := tlswrapper.NewServer(srvCfg)
	if err != nil {
		t.Fatal("server create:", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal("server start:", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// Client: identity.mux_connect dials the server; identity.listen["server"]
	// binds a local port that forwards through the session to that peer.
	cliCfg := newPlaintextConfig(t, map[string]any{
		"identity": map[string]any{
			"claim":       "client",
			"mux_connect": []string{muxAddr},
			"listen":      map[string]any{"server": clientListenAddr},
		},
	})
	cli, err := tlswrapper.NewServer(cliCfg)
	if err != nil {
		t.Fatal("client create:", err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal("client start:", err)
	}
	t.Cleanup(func() { _ = cli.Shutdown() })

	// Wait for the outbound mux session to be established.
	waitFor(t, 5*time.Second, func() bool { return cli.Stats().NumSessions > 0 })

	// Connect through the identity.listen["server"] port and verify echo.
	conn, err := net.DialTimeout("tcp", clientListenAddr, 3*time.Second)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer conn.Close()

	want := []byte("hello identity listen mux connect")
	if _, err := conn.Write(want); err != nil {
		t.Fatal("write:", err)
	}
	got := make([]byte, len(want))
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal("read:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}
}

func TestShutdownClosesServiceListeners(t *testing.T) {
	listenAddr := freePort(t)

	cfg := newPlaintextConfig(t, map[string]any{
		"identity": map[string]any{
			"listen": map[string]any{
				"peer-a": listenAddr,
			},
		},
	})
	srv, err := tlswrapper.NewServer(cfg)
	if err != nil {
		t.Fatal("server create:", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal("server start:", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Shutdown()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal("server shutdown:", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server shutdown blocked with service listener active")
	}
}

// freeUDPPort returns an available UDP address on localhost by briefly
// binding to :0 and immediately closing the listener.
func freeUDPPort(t *testing.T) string {
	t.Helper()
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.LocalAddr().String()
	_ = l.Close()
	return addr
}

// newH3TLSPair generates a self-signed ECDSA P256 certificate for each side
// (server and client), both with IP SAN 127.0.0.1.
// Returns the PEM-encoded cert and key for each side.
func newH3TLSPair(t *testing.T) (serverCertPEM, serverKeyPEM, clientCertPEM, clientKeyPEM string) {
	t.Helper()
	makeCert := func(cn string) (certPEM, keyPEM string) {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("ecdsa.GenerateKey: %v", err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
		if err != nil {
			t.Fatalf("x509.CreateCertificate: %v", err)
		}
		privDER, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			t.Fatalf("x509.MarshalECPrivateKey: %v", err)
		}
		certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
		keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}))
		return
	}
	serverCertPEM, serverKeyPEM = makeCert("h3mux-server")
	clientCertPEM, clientKeyPEM = makeCert("h3mux-client")
	return
}

// TestForwardH3MuxBidirectional verifies end-to-end bidirectional TCP
// forwarding through an h3mux (QUIC+TLS) session using self-signed certificates:
//
//	[test conn] → [client Listen] ──h3mux/QUIC──> [server MuxListen] → [echo server]
func TestForwardH3MuxBidirectional(t *testing.T) {
	echoAddr := startEchoServer(t)
	muxAddr := freeUDPPort(t)
	clientListenAddr := freePort(t)
	serverCertPEM, serverKeyPEM, clientCertPEM, clientKeyPEM := newH3TLSPair(t)

	// Session server: accepts h3mux sessions, forwards streams to the echo server.
	srvCfg := newPlaintextConfig(t, map[string]any{
		"mux_protocol": "h3mux",
		"mux_listen":   muxAddr,
		"connect":      echoAddr,
		"tls": map[string]any{
			"cert":      serverCertPEM,
			"key":       serverKeyPEM,
			"authcerts": []string{clientCertPEM},
			"sni":       "127.0.0.1", // test certs carry an IP SAN only
		},
	})
	srv, err := tlswrapper.NewServer(srvCfg)
	if err != nil {
		t.Fatal("server create:", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal("server start:", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// Session client: dials the h3mux server, exposes a local TCP listener.
	cliCfg := newPlaintextConfig(t, map[string]any{
		"mux_protocol": "h3mux",
		"mux_connect":  muxAddr,
		"listen":       clientListenAddr,
		"identity":     map[string]any{"claim": "test-client"},
		"tls": map[string]any{
			"cert":      clientCertPEM,
			"key":       clientKeyPEM,
			"authcerts": []string{serverCertPEM},
			"sni":       "127.0.0.1", // test certs carry an IP SAN only
		},
	})
	cli, err := tlswrapper.NewServer(cliCfg)
	if err != nil {
		t.Fatal("client create:", err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal("client start:", err)
	}
	t.Cleanup(func() { _ = cli.Shutdown() })

	// Wait for the outbound h3mux session to be established (up to 5 s).
	waitFor(t, 5*time.Second, func() bool { return cli.Stats().NumSessions > 0 })

	// Connect through the session and verify bidirectional forwarding.
	conn, err := net.DialTimeout("tcp", clientListenAddr, 3*time.Second)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer conn.Close()

	want := []byte("hello h3mux bidirectional forwarding")
	if _, err := conn.Write(want); err != nil {
		t.Fatal("write:", err)
	}
	got := make([]byte, len(want))
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal("read:", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}
}
