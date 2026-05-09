// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
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

func TestGrpcStreamWriteTimeout(t *testing.T) {
	unblock := make(chan struct{})
	aborted := make(chan struct{}, 1)
	stream := &grpcStream{
		sender: stubChunkSender{send: func(*muxpb.Chunk) error {
			<-unblock
			return errors.New("aborted")
		}},
		recver:     stubChunkRecver{},
		closeWrite: func() error { return nil },
		abortWrite: func() {
			select {
			case aborted <- struct{}{}:
			default:
			}
			close(unblock)
		},
		writeTimer: 20 * time.Millisecond,
		doneCh:     make(chan struct{}),
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
