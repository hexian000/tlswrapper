// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"testing"

	"google.golang.org/grpc/stats"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

func TestMuxStatsHandlerHandleConn(t *testing.T) {
	h := &muxStatsHandler{}
	// HandleConn is a no-op; calling it verifies it doesn't panic.
	h.HandleConn(context.Background(), &stats.ConnBegin{})
	h.HandleConn(context.Background(), &stats.ConnEnd{})
}

func TestMuxStatsHandlerTagConn(t *testing.T) {
	h := &muxStatsHandler{}
	ctx := h.TagConn(context.Background(), &stats.ConnTagInfo{})
	if ctx == nil {
		t.Fatal("TagConn returned nil context")
	}
}

func TestMuxStatsHandlerTagRPC(t *testing.T) {
	h := &muxStatsHandler{}

	// Stream method: context should carry the tracking key.
	streamCtx := h.TagRPC(context.Background(), &stats.RPCTagInfo{
		FullMethodName: muxpb.Mux_Stream_FullMethodName,
	})
	if streamCtx.Value(ctxKeyTrackRPC{}) == nil {
		t.Fatal("TagRPC(Stream) did not mark context for tracking")
	}

	// Non-Stream method: context must not carry the tracking key.
	otherCtx := h.TagRPC(context.Background(), &stats.RPCTagInfo{
		FullMethodName: "/other.Service/Method",
	})
	if otherCtx.Value(ctxKeyTrackRPC{}) != nil {
		t.Fatal("TagRPC(non-Stream) unexpectedly marked context")
	}
}

func TestMuxStatsHandlerHandleRPC(t *testing.T) {
	h := &muxStatsHandler{}

	// Untracked context: stats should not change.
	h.HandleRPC(context.Background(), &stats.Begin{})
	if h.metrics.NumStreams.Load() != 0 {
		t.Fatal("HandleRPC on untracked context should not change NumStreams")
	}

	// Tracked context: exercise each stats type.
	tracked := context.WithValue(context.Background(), ctxKeyTrackRPC{}, struct{}{})

	h.HandleRPC(tracked, &stats.Begin{})
	if got := h.metrics.NumStreams.Load(); got != 1 {
		t.Fatalf("after Begin: NumStreams = %d, want 1", got)
	}

	h.HandleRPC(tracked, &stats.InPayload{Length: 10, WireLength: 12})
	if got := h.metrics.BytesReceived.Load(); got != 10 {
		t.Fatalf("BytesReceived = %d, want 10", got)
	}

	h.HandleRPC(tracked, &stats.OutPayload{Length: 20, WireLength: 22})
	if got := h.metrics.BytesSent.Load(); got != 20 {
		t.Fatalf("BytesSent = %d, want 20", got)
	}

	h.HandleRPC(tracked, &stats.End{Error: nil})
	if got := h.metrics.StreamsSucceeded.Load(); got != 1 {
		t.Fatalf("StreamsSucceeded = %d, want 1", got)
	}
	if got := h.metrics.NumStreams.Load(); got != 0 {
		t.Fatalf("after End: NumStreams = %d, want 0", got)
	}

	h.HandleRPC(tracked, &stats.Begin{})
	h.HandleRPC(tracked, &stats.End{Error: context.Canceled})
	if got := h.metrics.StreamsFailed.Load(); got != 1 {
		t.Fatalf("StreamsFailed = %d, want 1", got)
	}
}
