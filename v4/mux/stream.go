// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"io"
	"net"
	"sync"
	"time"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
)

// chunkPool pools *muxpb.Chunk values to reduce per-write allocations.
var chunkPool = sync.Pool{New: func() any { return &muxpb.Chunk{} }}

// h2Addr is a simple net.Addr implementation used as a fallback.
type h2Addr struct{ Addr string }

func (a h2Addr) Network() string { return "tcp" }
func (a h2Addr) String() string  { return a.Addr }

// chunkSender is the write side of a gRPC stream carrying Chunks.
type chunkSender interface {
	Send(*muxpb.Chunk) error
}

// chunkRecver is the read side of a gRPC stream carrying Chunks.
type chunkRecver interface {
	Recv() (*muxpb.Chunk, error)
}

// grpcStream is a single gRPC bidi stream that implements net.Conn.
// It wraps either a client-side or server-side Stream RPC.
//
// Half-close is only supported on the client side via CloseSend().
// Server-side CloseWrite is a no-op; the peer sees EOF when the handler returns.
//
// doneCh is closed on the first Close() call. Server-side Stream handlers
// (which must stay alive while the stream is in use) wait on doneCh.
type grpcStream struct {
	sender     chunkSender
	recver     chunkRecver
	closeWrite func() error // half-close write side
	onClose    func()       // called once on first Close()

	readBuf    []byte
	localAddr  net.Addr
	remoteAddr net.Addr

	doneCh    chan struct{}
	closeOnce sync.Once
}

// Read implements net.Conn.
func (s *grpcStream) Read(b []byte) (int, error) {
	for {
		if len(s.readBuf) > 0 {
			n := copy(b, s.readBuf)
			s.readBuf = s.readBuf[n:]
			return n, nil
		}
		chunk, err := s.recver.Recv()
		if err != nil {
			if err == io.EOF {
				return 0, io.EOF
			}
			return 0, err
		}
		if len(chunk.Data) > 0 {
			s.readBuf = chunk.Data
		}
	}
}

// Write implements net.Conn.
func (s *grpcStream) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c := chunkPool.Get().(*muxpb.Chunk)
	c.Data = b
	err := s.sender.Send(c)
	c.Data = nil
	chunkPool.Put(c)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// CloseWrite half-closes the write side.
func (s *grpcStream) CloseWrite() error {
	return s.closeWrite()
}

// Close fully closes the stream: half-closes the write side and signals
// the server-side Stream handler (via doneCh) that it may return.
func (s *grpcStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.closeWrite()
		close(s.doneCh)
		if s.onClose != nil {
			s.onClose()
		}
	})
	return nil
}

func (s *grpcStream) LocalAddr() net.Addr  { return s.localAddr }
func (s *grpcStream) RemoteAddr() net.Addr { return s.remoteAddr }

// Deadline methods are no-ops; flow control is handled at the gRPC/transport level.
func (s *grpcStream) SetDeadline(t time.Time) error      { return nil }
func (s *grpcStream) SetReadDeadline(t time.Time) error  { return nil }
func (s *grpcStream) SetWriteDeadline(t time.Time) error { return nil }

// newClientSideStream wraps a client-side gRPC Stream RPC as a net.Conn.
// Half-close uses CloseSend().
func newClientSideStream(
	cs muxpb.Mux_StreamClient,
	localAddr, remoteAddr net.Addr,
	onClose func(),
) net.Conn {
	return &grpcStream{
		sender:     cs,
		recver:     cs,
		closeWrite: cs.CloseSend,
		onClose:    onClose,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		doneCh:     make(chan struct{}),
	}
}

// newServerSideStream wraps a server-side gRPC Stream RPC as a net.Conn.
// CloseWrite is a no-op; the peer sees EOF when the handler returns.
// The returned *grpcStream's doneCh is closed by Close(), allowing the
// server-side Stream handler to detect when it may return.
func newServerSideStream(
	ss muxpb.Mux_StreamServer,
	localAddr, remoteAddr net.Addr,
	onClose func(),
) *grpcStream {
	return &grpcStream{
		sender:     ss,
		recver:     ss,
		closeWrite: func() error { return nil },
		onClose:    onClose,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		doneCh:     make(chan struct{}),
	}
}
