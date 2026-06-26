// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/mem"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// h2Addr is a simple net.Addr implementation used as a fallback.
type h2Addr struct{ Addr string }

func (a h2Addr) Network() string { return "tcp" }
func (a h2Addr) String() string  { return a.Addr }

// streamRW is the subset of grpc.ClientStream/ServerStream used to exchange
// rawChunk messages. Bypassing the generated Send/Recv wrappers lets
// rawChunkCodec handle the data path without protobuf message objects.
type streamRW interface {
	SendMsg(m any) error
	RecvMsg(m any) error
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
	rw         streamRW
	closeWrite func() error // half-close write side
	onClose    func()       // called once on first Close()
	abortWrite func()       // called when a blocked write times out
	abortRead  func()       // called when a blocked read times out

	// readBufs holds unconsumed received data as references into transport
	// buffers; each is freed as it is drained.
	readBufs   mem.BufferSlice
	localAddr  net.Addr
	remoteAddr net.Addr

	mu            sync.RWMutex
	readDeadline  time.Time
	writeDeadline time.Time
	doneCh        chan struct{}
	closeOnce     sync.Once

	// writeMu serializes Send and CloseSend on the underlying gRPC stream:
	// grpc-go forbids calling CloseSend concurrently with SendMsg (or itself,
	// as happens when one forwarder direction half-closes while the other
	// force-closes). closeWriteOnce ensures CloseSend runs at most once.
	writeMu        sync.Mutex
	closeWriteOnce sync.Once
}

func (s *grpcStream) Read(b []byte) (int, error) {
	for {
		for len(s.readBufs) > 0 {
			buf := s.readBufs[0]
			if buf.Len() > 0 {
				n, rest := mem.ReadUnsafe(b, buf)
				if rest == nil {
					s.readBufs = s.readBufs[1:]
				} else {
					s.readBufs[0] = rest
				}
				return n, nil
			}
			buf.Free()
			s.readBufs = s.readBufs[1:]
		}
		var chunk rawChunk
		if err := s.recvChunk(&chunk); err != nil {
			return 0, err
		}
		s.readBufs = chunk.bufs
	}
}

func (s *grpcStream) recvChunk(chunk *rawChunk) error {
	if timeout, ok := s.readTimeout(); ok {
		if timeout <= 0 {
			if s.abortRead != nil {
				s.abortRead()
			}
			return os.ErrDeadlineExceeded
		}
		type result struct {
			chunk rawChunk
			err   error
		}
		resCh := make(chan result, 1)
		go func() {
			var rc rawChunk
			err := s.rw.RecvMsg(&rc)
			resCh <- result{rc, err}
		}()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case res := <-resCh:
			*chunk = res.chunk
			return res.err
		case <-timer.C:
			if s.abortRead != nil {
				s.abortRead()
			}
			return os.ErrDeadlineExceeded
		}
	}
	return s.rw.RecvMsg(chunk)
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.rw.SendMsg(&rawChunk{payload: data})
}

// doCloseWrite invokes closeWrite at most once, serialized against Send.
func (s *grpcStream) doCloseWrite() error {
	var err error
	s.closeWriteOnce.Do(func() {
		s.writeMu.Lock()
		err = s.closeWrite()
		s.writeMu.Unlock()
	})
	return err
}

func (s *grpcStream) CloseWrite() error {
	return s.doCloseWrite()
}

// Close releases any waiter on doneCh, aborts the RPC, and closes the write
// side.  onClose (the RPC cancel) runs before doCloseWrite so that an
// in-flight Send holding writeMu is unblocked instead of deadlocking Close.
func (s *grpcStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.doneCh)
		if s.onClose != nil {
			s.onClose()
		}
		_ = s.doCloseWrite()
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
		rw:         cs,
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
		rw:         ss,
		closeWrite: func() error { return nil },
		onClose:    onClose,
		abortWrite: abortWrite,
		abortRead:  abortWrite,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		doneCh:     make(chan struct{}),
	}
}
