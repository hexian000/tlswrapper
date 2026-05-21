// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

type stubChunkSender struct {
	send func(*muxpb.Chunk) error
}

func (s stubChunkSender) Send(chunk *muxpb.Chunk) error {
	return s.send(chunk)
}

type stubChunkRecver struct{}

func (stubChunkRecver) Recv() (*muxpb.Chunk, error) { return nil, io.EOF }

// blockingChunkRecver blocks until unblock is closed, then returns an error.
type blockingChunkRecver struct{ unblock chan struct{} }

func (r *blockingChunkRecver) Recv() (*muxpb.Chunk, error) {
	<-r.unblock
	return nil, errors.New("aborted")
}

func TestGrpcStreamSetWriteDeadline(t *testing.T) {
	stream := &grpcStream{
		sender:     stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:     stubChunkRecver{},
		closeWrite: func() error { return nil },
		doneCh:     make(chan struct{}),
	}
	deadline := time.Now().Add(time.Second)
	if err := stream.SetWriteDeadline(deadline); err != nil {
		t.Fatalf("SetWriteDeadline() error = %v", err)
	}
	stream.mu.RLock()
	got := stream.writeDeadline
	stream.mu.RUnlock()
	if !got.Equal(deadline) {
		t.Fatalf("writeDeadline = %v, want %v", got, deadline)
	}
	if _, err := stream.Write([]byte("payload")); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}
}

func TestGrpcStreamReadTimeout(t *testing.T) {
	unblock := make(chan struct{})
	aborted := make(chan struct{}, 1)
	stream := &grpcStream{
		sender:     stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:     &blockingChunkRecver{unblock: unblock},
		closeWrite: func() error { return nil },
		abortRead: func() {
			select {
			case aborted <- struct{}{}:
			default:
			}
			close(unblock)
		},
		doneCh:       make(chan struct{}),
		readDeadline: time.Now().Add(20 * time.Millisecond),
	}

	start := time.Now()
	buf := make([]byte, 4)
	_, err := stream.Read(buf)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read() error = %v, want %v", err, os.ErrDeadlineExceeded)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("Read() returned too early after %v", elapsed)
	}
	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("abortRead was not called")
	}
}

func TestGrpcStreamSetReadDeadline(t *testing.T) {
	stream := &grpcStream{
		sender:     stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:     stubChunkRecver{},
		closeWrite: func() error { return nil },
		doneCh:     make(chan struct{}),
	}
	deadline := time.Now().Add(time.Second)
	if err := stream.SetReadDeadline(deadline); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	stream.mu.RLock()
	got := stream.readDeadline
	stream.mu.RUnlock()
	if !got.Equal(deadline) {
		t.Fatalf("readDeadline = %v, want %v", got, deadline)
	}
}

func TestGrpcStreamSetDeadline(t *testing.T) {
	stream := &grpcStream{
		sender:     stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:     stubChunkRecver{},
		closeWrite: func() error { return nil },
		doneCh:     make(chan struct{}),
	}
	deadline := time.Now().Add(time.Second)
	if err := stream.SetDeadline(deadline); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	stream.mu.RLock()
	gotR := stream.readDeadline
	gotW := stream.writeDeadline
	stream.mu.RUnlock()
	if !gotR.Equal(deadline) {
		t.Fatalf("readDeadline = %v, want %v", gotR, deadline)
	}
	if !gotW.Equal(deadline) {
		t.Fatalf("writeDeadline = %v, want %v", gotW, deadline)
	}
}

func TestH2Addr(t *testing.T) {
	a := h2Addr{Addr: "192.0.2.1:443"}
	if got := a.Network(); got != "tcp" {
		t.Fatalf("Network() = %q, want %q", got, "tcp")
	}
	if got := a.String(); got != "192.0.2.1:443" {
		t.Fatalf("String() = %q, want %q", got, "192.0.2.1:443")
	}
}

func TestGrpcStreamLocalRemoteAddr(t *testing.T) {
	local := h2Addr{Addr: "127.0.0.1:1234"}
	remote := h2Addr{Addr: "127.0.0.1:5678"}
	stream := &grpcStream{
		sender:     stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:     stubChunkRecver{},
		closeWrite: func() error { return nil },
		doneCh:     make(chan struct{}),
		localAddr:  local,
		remoteAddr: remote,
	}
	if got := stream.LocalAddr(); got != local {
		t.Fatalf("LocalAddr() = %v, want %v", got, local)
	}
	if got := stream.RemoteAddr(); got != remote {
		t.Fatalf("RemoteAddr() = %v, want %v", got, remote)
	}
}

func TestGrpcStreamCloseWrite(t *testing.T) {
	called := false
	stream := &grpcStream{
		sender:     stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:     stubChunkRecver{},
		closeWrite: func() error { called = true; return nil },
		doneCh:     make(chan struct{}),
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if !called {
		t.Fatal("CloseWrite did not invoke the underlying closeWrite function")
	}
}

func TestGrpcStreamWriteDeadlineExpired(t *testing.T) {
	stream := &grpcStream{
		sender:        stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:        stubChunkRecver{},
		closeWrite:    func() error { return nil },
		doneCh:        make(chan struct{}),
		writeDeadline: time.Now().Add(-time.Second), // already in the past
	}
	_, err := stream.Write([]byte("payload"))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Write() error = %v, want %v", err, os.ErrDeadlineExceeded)
	}
}

func TestGrpcStreamReadDeadlineExpired(t *testing.T) {
	stream := &grpcStream{
		sender:       stubChunkSender{send: func(*muxpb.Chunk) error { return nil }},
		recver:       stubChunkRecver{},
		closeWrite:   func() error { return nil },
		doneCh:       make(chan struct{}),
		readDeadline: time.Now().Add(-time.Second), // already in the past
	}
	_, err := stream.Read(make([]byte, 4))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read() error = %v, want %v", err, os.ErrDeadlineExceeded)
	}
}

// blockingChunkSender blocks until unblock is closed, then returns an error.
type blockingChunkSender struct{ unblock chan struct{} }

func (s *blockingChunkSender) Send(*muxpb.Chunk) error {
	<-s.unblock
	return errors.New("aborted")
}

func TestGrpcStreamWriteTimeout(t *testing.T) {
	unblock := make(chan struct{})
	aborted := make(chan struct{}, 1)
	stream := &grpcStream{
		sender:     &blockingChunkSender{unblock: unblock},
		recver:     stubChunkRecver{},
		closeWrite: func() error { return nil },
		abortWrite: func() {
			select {
			case aborted <- struct{}{}:
			default:
			}
			close(unblock)
		},
		doneCh:        make(chan struct{}),
		writeDeadline: time.Now().Add(20 * time.Millisecond),
	}
	start := time.Now()
	_, err := stream.Write([]byte("payload"))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Write() error = %v, want %v", err, os.ErrDeadlineExceeded)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("Write() returned too early after %v", elapsed)
	}
	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("abortWrite was not called")
	}
}
