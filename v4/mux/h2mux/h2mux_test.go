// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	mux "github.com/hexian000/tlswrapper/v4/mux"
)

// workloadChunk is the per-write size the throughput benchmarks use. It matches
// the forwarder's io.CopyBuffer buffer (forwarder.copyBufPool, 64 KiB), which is
// the largest contiguous write tlswrapper issues on the data path, so the
// benchmarks reflect a realistic production write size rather than an arbitrary
// large block.
const workloadChunk = 64 * 1024

// benchSelfSignedTLS creates a minimal self-signed RSA-4096 certificate pair for
// benchmarking. The client side skips verification for simplicity.
//
// Both sides pin MinVersion to TLS 1.3, whose cipher suites are not
// configurable via tls.Config.CipherSuites. With Go's default preference on
// amd64 (AES-NI present), the handshake negotiates TLS_AES_128_GCM_SHA256
// (AES-128-GCM AEAD, SHA-256), which is the suite exercised by this benchmark.
func benchSelfSignedTLS(b *testing.B) (serverTLS, clientTLS *tls.Config) {
	b.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		b.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "h2mux-bench"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		b.Fatalf("x509.CreateCertificate: %v", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(priv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		b.Fatalf("tls.X509KeyPair: %v", err)
	}
	serverTLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	clientTLS = &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // bench-only
		MinVersion:         tls.VersionTLS13,
	}
	return serverTLS, clientTLS
}

// benchTLSSessionPair establishes a pair of connected mux Sessions over a real
// TCP loopback connection secured with TLS, using otherwise default config.
// Both sessions are closed via b.Cleanup when the benchmark ends.
func benchTLSSessionPair(b *testing.B) (cli, srv mux.Session) {
	b.Helper()
	serverTLS, clientTLS := benchSelfSignedTLS(b)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("net.Listen: %v", err)
	}
	b.Cleanup(func() { _ = l.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	b.Cleanup(cancel)

	type result struct {
		sess mux.Session
		err  error
	}
	srvCh := make(chan result, 1)
	go func() {
		rawConn, err := l.Accept()
		if err != nil {
			srvCh <- result{nil, err}
			return
		}
		sess, err := Server(ctx, rawConn, &Config{LocalID: "srv", TLSConfig: serverTLS})
		srvCh <- result{sess, err}
	}()

	rawConn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		b.Fatalf("net.Dial: %v", err)
	}
	cliSess, err := Client(ctx, rawConn, &Config{LocalID: "cli", TLSConfig: clientTLS})
	if err != nil {
		b.Fatalf("mux.Client: %v", err)
	}

	res := <-srvCh
	if res.err != nil {
		_ = cliSess.Close()
		b.Fatalf("mux.Server: %v", res.err)
	}

	b.Cleanup(func() {
		_ = cliSess.Close()
		_ = res.sess.Close()
	})
	return cliSess, res.sess
}

// BenchmarkTCPThroughput is the lowest baseline: one-way throughput over a bare
// TCP loopback connection, with no TLS or mux/gRPC layered on top. Same
// workloadChunk transfer pattern as the other throughput benchmarks, so the TLS
// and mux overheads are the successive drops from this number.
func BenchmarkTCPThroughput(b *testing.B) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("net.Listen: %v", err)
	}
	b.Cleanup(func() { _ = l.Close() })

	srvCh := make(chan net.Conn, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			srvCh <- nil
			return
		}
		srvCh <- conn
	}()

	cliConn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		b.Fatalf("net.Dial: %v", err)
	}
	srvConn := <-srvCh
	if srvConn == nil {
		b.Fatal("listener Accept returned nil")
	}
	b.Cleanup(func() {
		_ = cliConn.Close()
		_ = srvConn.Close()
	})

	const chunk = workloadChunk
	writeBuf := make([]byte, chunk)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := cliConn.Write(writeBuf); err != nil {
				return
			}
		}
	}()

	readBuf := make([]byte, chunk)
	b.SetBytes(chunk)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(srvConn, readBuf); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
	b.StopTimer()
	close(done)
	_ = cliConn.Close()
}

// BenchmarkTLSThroughput is the baseline: one-way throughput over a bare TLS 1.3
// connection on TCP loopback, with no mux/gRPC layered on top. Same RSA-4096
// cert, cipher suite (TLS_AES_128_GCM_SHA256) and transfer pattern as
// BenchmarkStreamThroughputTLS, so the two numbers are directly comparable and
// the mux overhead is the difference between them.
func BenchmarkTLSThroughput(b *testing.B) {
	serverTLS, clientTLS := benchSelfSignedTLS(b)

	l, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		b.Fatalf("tls.Listen: %v", err)
	}
	b.Cleanup(func() { _ = l.Close() })

	// The server side must drive its handshake concurrently with the client's
	// tls.Dial: tls.Dial blocks until the handshake completes, and the accepted
	// tls.Conn only handshakes lazily on first I/O, so without an explicit
	// Handshake() here the two sides would deadlock.
	srvCh := make(chan net.Conn, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			srvCh <- nil
			return
		}
		if err := conn.(*tls.Conn).Handshake(); err != nil {
			_ = conn.Close()
			srvCh <- nil
			return
		}
		srvCh <- conn
	}()

	cliConn, err := tls.Dial("tcp", l.Addr().String(), clientTLS)
	if err != nil {
		b.Fatalf("tls.Dial: %v", err)
	}
	srvConn := <-srvCh
	if srvConn == nil {
		b.Fatal("listener Accept returned nil")
	}
	b.Cleanup(func() {
		_ = cliConn.Close()
		_ = srvConn.Close()
	})

	const chunk = workloadChunk
	writeBuf := make([]byte, chunk)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := cliConn.Write(writeBuf); err != nil {
				return
			}
		}
	}()

	readBuf := make([]byte, chunk)
	b.SetBytes(chunk)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(srvConn, readBuf); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
	b.StopTimer()
	close(done)
	_ = cliConn.Close()
}

// BenchmarkStreamThroughputTLS measures one-way stream throughput over a single
// h2mux stream carried by a default-config TLS connection on TCP loopback.
// A dedicated writer goroutine feeds the client side while the benchmark loop
// drains the server side; b.SetBytes reports MB/s.
func BenchmarkStreamThroughputTLS(b *testing.B) {
	cli, srv := benchTLSSessionPair(b)
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
		b.Fatalf("cli.Open: %v", err)
	}
	srvConn := <-acceptCh
	if srvConn == nil {
		b.Fatal("srv.Accept returned nil")
	}
	b.Cleanup(func() {
		_ = cliConn.Close()
		_ = srvConn.Close()
	})

	const chunk = workloadChunk
	writeBuf := make([]byte, chunk)

	// Continuous writer: keeps the pipe full so the benchmark loop measures
	// sustained read throughput rather than per-iteration write latency.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := cliConn.Write(writeBuf); err != nil {
				return
			}
		}
	}()

	readBuf := make([]byte, chunk)
	b.SetBytes(chunk)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(srvConn, readBuf); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
	b.StopTimer()
	close(done)
	// Closing the stream unblocks the writer if it is parked in Write.
	_ = cliConn.Close()
}
