// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"io"
	"net"
	"os"
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

type chunkSender interface {
	Send(*muxpb.Chunk) error
}

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
	abortWrite func()       // called when a blocked write times out
	abortRead  func()       // called when a blocked read times out

	readBuf    []byte
	localAddr  net.Addr
	remoteAddr net.Addr

	mu            sync.RWMutex
	readDeadline  time.Time
	writeDeadline time.Time
	doneCh        chan struct{}
	closeOnce     sync.Once
}

func (s *grpcStream) Read(b []byte) (int, error) {
	for {
		if len(s.readBuf) > 0 {
			n := copy(b, s.readBuf)
			s.readBuf = s.readBuf[n:]
			return n, nil
		}
		chunk, err := s.recvChunk()
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

func (s *grpcStream) recvChunk() (*muxpb.Chunk, error) {
	if timeout, ok := s.readTimeout(); ok {
		if timeout <= 0 {
			if s.abortRead != nil {
				s.abortRead()
			}
			return nil, os.ErrDeadlineExceeded
		}
		type result struct {
			chunk *muxpb.Chunk
			err   error
		}
		resCh := make(chan result, 1)
		go func() {
			chunk, err := s.recver.Recv()
			resCh <- result{chunk, err}
		}()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case res := <-resCh:
			return res.chunk, res.err
		case <-timer.C:
			if s.abortRead != nil {
				s.abortRead()
			}
			return nil, os.ErrDeadlineExceeded
		}
	}
	return s.recver.Recv()
}

func (s *grpcStream) readTimeout() (time.Duration, bool) {
	s.mu.RLock()
	deadline := s.readDeadline
	s.mu.RUnlock()
	if !deadline.IsZero() {
		return time.Until(deadline), true
	}
	return 0, false
}

func (s *grpcStream) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if timeout, ok := s.writeTimeout(); ok {
		if timeout <= 0 {
			if s.abortWrite != nil {
				s.abortWrite()
			}
			return 0, os.ErrDeadlineExceeded
		}
		data := append([]byte(nil), b...)
		errCh := make(chan error, 1)
		go func() {
			errCh <- s.sendChunk(data)
		}()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case err := <-errCh:
			if err != nil {
				return 0, err
			}
			return len(b), nil
		case <-timer.C:
			if s.abortWrite != nil {
				s.abortWrite()
			}
			return 0, os.ErrDeadlineExceeded
		}
	}
	err := s.sendChunk(b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (s *grpcStream) sendChunk(data []byte) error {
	c := chunkPool.Get().(*muxpb.Chunk)
	c.Data = data
	err := s.sender.Send(c)
	c.Data = nil
	chunkPool.Put(c)
	return err
}

func (s *grpcStream) CloseWrite() error {
	return s.closeWrite()
}

// Close half-closes the write side and releases any waiter on doneCh.
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

func (s *grpcStream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	s.writeDeadline = t
	return nil
}

func (s *grpcStream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	return nil
}

func (s *grpcStream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDeadline = t
	return nil
}

func (s *grpcStream) writeTimeout() (time.Duration, bool) {
	s.mu.RLock()
	deadline := s.writeDeadline
	s.mu.RUnlock()
	if !deadline.IsZero() {
		return time.Until(deadline), true
	}
	return 0, false
}

// newClientSideStream wraps a client-side gRPC Stream RPC as a net.Conn.
// Half-close uses CloseSend().
func newClientSideStream(
	cs muxpb.Mux_StreamClient,
	localAddr, remoteAddr net.Addr,
	onClose func(),
	abortWrite func(),
) net.Conn {
	return &grpcStream{
		sender:     cs,
		recver:     cs,
		closeWrite: cs.CloseSend,
		onClose:    onClose,
		abortWrite: abortWrite,
		abortRead:  abortWrite,
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
	abortWrite func(),
) *grpcStream {
	return &grpcStream{
		sender:     ss,
		recver:     ss,
		closeWrite: func() error { return nil },
		onClose:    onClose,
		abortWrite: abortWrite,
		abortRead:  abortWrite,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		doneCh:     make(chan struct{}),
	}
}
