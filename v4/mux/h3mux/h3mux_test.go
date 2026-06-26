// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux_test

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/quic-go/quic-go"

	. "github.com/hexian000/tlswrapper/v4/mux/h3mux"
)

// workloadChunk is the per-write size the stream throughput benchmarks use. It
// matches the forwarder's io.CopyBuffer buffer (forwarder.copyBufPool, 64 KiB),
// the largest contiguous write tlswrapper issues on the data path, so the
// benchmarks reflect a realistic production write size. The bare-UDP baseline
// cannot use it: a 64 KiB datagram exceeds the 65507-byte IPv4 UDP payload
// limit, so it uses a smaller datagram (see BenchmarkUDPThroughput).
const workloadChunk = 64 * 1024

// benchThroughput drives a one-way throughput measurement: a dedicated writer
// goroutine floods w with fixed-size chunks while the benchmark loop drains r,
// one chunk per iteration. b.SetBytes reports MB/s.
//
// The writer tolerates transient write errors (e.g. ENOBUFS when flooding a UDP
// loopback socket) and only exits once done is closed, so a dropped datagram
// never stalls the reader.
func benchThroughput(b *testing.B, w io.Writer, r io.Reader, closeWriter func() error, chunk int) {
	b.Helper()
	writeBuf := make([]byte, chunk)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := w.Write(writeBuf); err != nil {
				select {
				case <-done:
					return
				default:
				}
			}
		}
	}()

	readBuf := make([]byte, chunk)
	b.SetBytes(int64(chunk))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(r, readBuf); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
	b.StopTimer()
	close(done)
	if closeWriter != nil {
		_ = closeWriter()
	}
}

// BenchmarkUDPThroughput is the lowest baseline: one-way throughput over a bare
// UDP loopback socket, with no QUIC/TLS or mux layered on top. Datagrams are
// 32 KiB: the 64 KiB workloadChunk used by the other benchmarks cannot fit in a
// single datagram (the IPv4 UDP payload limit is 65507 bytes). Throughput is
// measured on the delivered (read) side, so packets dropped under flooding do
// not affect correctness, only the rate.
func BenchmarkUDPThroughput(b *testing.B) {
	srvConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatalf("net.ListenUDP: %v", err)
	}
	b.Cleanup(func() { _ = srvConn.Close() })
	// Enlarge the receive buffer to limit drops under loopback flooding.
	_ = srvConn.SetReadBuffer(4 << 20)

	cliConn, err := net.DialUDP("udp", nil, srvConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		b.Fatalf("net.DialUDP: %v", err)
	}
	b.Cleanup(func() { _ = cliConn.Close() })

	const dgram = 32 * 1024
	benchThroughput(b, cliConn, srvConn, cliConn.Close, dgram)
}

// BenchmarkQUICThroughput is the middle baseline: one-way throughput over a
// single bare QUIC stream (TLS 1.3, TLS_AES_128_GCM_SHA256), with no h3mux
// control stream or framing on top. Same workloadChunk transfer pattern as
// BenchmarkStreamThroughput, so the h3mux overhead is the difference.
func BenchmarkQUICThroughput(b *testing.B) {
	serverTLS, clientTLS := generateSelfSignedTLS(b)
	// QUIC mandates an ALPN; both sides advertise the same single protocol.
	serverTLS.NextProtos = []string{defaultBenchALPN}
	clientTLS.NextProtos = []string{defaultBenchALPN}
	qconf := &quic.Config{MaxIncomingStreams: 1024, MaxIncomingUniStreams: -1}

	listener, err := quic.ListenAddr("127.0.0.1:0", serverTLS, qconf)
	if err != nil {
		b.Fatalf("quic.ListenAddr: %v", err)
	}
	b.Cleanup(func() { _ = listener.Close() })

	ctx := context.Background()
	type srvRes struct {
		conn *quic.Conn
		err  error
	}
	srvCh := make(chan srvRes, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		srvCh <- srvRes{conn, err}
	}()

	cliConn, err := quic.DialAddr(ctx, listener.Addr().String(), clientTLS, qconf)
	if err != nil {
		b.Fatalf("quic.DialAddr: %v", err)
	}
	b.Cleanup(func() { _ = cliConn.CloseWithError(0, "bench done") })

	srv := <-srvCh
	if srv.err != nil {
		b.Fatalf("listener.Accept: %v", srv.err)
	}
	b.Cleanup(func() { _ = srv.conn.CloseWithError(0, "bench done") })

	cliStream, err := cliConn.OpenStreamSync(ctx)
	if err != nil {
		b.Fatalf("OpenStreamSync: %v", err)
	}
	// QUIC delivers a stream to the peer only once the opener sends data; prime
	// with a single byte so AcceptStream returns, then strip it on the far side.
	if _, err := cliStream.Write([]byte{0}); err != nil {
		b.Fatalf("prime write: %v", err)
	}
	srvStream, err := srv.conn.AcceptStream(ctx)
	if err != nil {
		b.Fatalf("AcceptStream: %v", err)
	}
	if _, err := io.ReadFull(srvStream, make([]byte, 1)); err != nil {
		b.Fatalf("prime read: %v", err)
	}

	benchThroughput(b, cliStream, srvStream, cliStream.Close, workloadChunk)
}

// BenchmarkStreamThroughput measures one-way throughput over a single h3mux
// stream carried by a default-config QUIC connection on UDP loopback. Compare
// against BenchmarkQUICThroughput (bare QUIC stream) and BenchmarkUDPThroughput
// (bare UDP) to isolate the QUIC/TLS and h3mux layer costs.
func BenchmarkStreamThroughput(b *testing.B) {
	cli, srv := quicSessions(b, &Config{LocalID: "cli"}, &Config{LocalID: "srv"})
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

	benchThroughput(b, cliConn, srvConn, cliConn.Close, workloadChunk)
}

// defaultBenchALPN is the ALPN advertised by the bare-QUIC benchmark. It only
// needs to match on both ends; "h3" mirrors h3mux's production default.
const defaultBenchALPN = "h3"
