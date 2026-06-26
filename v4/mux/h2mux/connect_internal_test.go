// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// fakeControlServer is a minimal muxpb.Mux_ControlServer for driving the
// Control handler directly. Recv reads from the recv channel (io.EOF when
// closed); only the methods used by Control are implemented.
type fakeControlServer struct {
	grpc.ServerStream // nil; unused methods must not be called
	ctx               context.Context
	recv              chan *muxpb.ControlMessage
}

func (f *fakeControlServer) Context() context.Context { return f.ctx }

func (f *fakeControlServer) Send(*muxpb.ControlMessage) error { return nil }

func (f *fakeControlServer) Recv() (*muxpb.ControlMessage, error) {
	msg, ok := <-f.recv
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

// TestMuxServerDuplicateControl verifies that a second Control RPC on the
// same connection is rejected with an error instead of panicking on the
// second close of sessReady (which would kill the process).
func TestMuxServerDuplicateControl(t *testing.T) {
	svc := newMuxServer(&Config{LocalID: "srv"}, nil, nil, newMuxStatsHandler())
	ctx := context.Background()

	first := &fakeControlServer{ctx: ctx, recv: make(chan *muxpb.ControlMessage, 1)}
	first.recv <- clientHelloMsg("cli", false)
	done := make(chan error, 1)
	go func() { done <- svc.Control(first) }()

	var sess *serverSession
	select {
	case sess = <-svc.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("first Control did not become ready")
	}

	second := &fakeControlServer{ctx: ctx, recv: make(chan *muxpb.ControlMessage, 1)}
	second.recv <- clientHelloMsg("cli", false)
	if err := svc.Control(second); !errors.Is(err, errDuplicateControl) {
		t.Fatalf("second Control = %v, want errDuplicateControl", err)
	}

	_ = sess.Close()
	close(first.recv)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first Control did not return after session close")
	}
}
