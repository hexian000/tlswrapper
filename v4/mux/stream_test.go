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
