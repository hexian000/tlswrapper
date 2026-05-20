// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc/stats"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
)

// SessionMetrics tracks per-session stream counts plus payload and wire bytes.
type SessionMetrics struct {
	StreamsOpened      atomic.Uint64 // streams actively opened by this endpoint
	StreamsAccepted    atomic.Uint64 // streams passively accepted by this endpoint
	StreamsSucceeded   atomic.Uint64
	StreamsFailed      atomic.Uint64
	NumStreams         atomic.Int64
	BytesSent          atomic.Uint64
	BytesReceived      atomic.Uint64
	WireLengthSent     atomic.Uint64
	WireLengthReceived atomic.Uint64
}

// ctxKeyTrackRPC is used as a context key to mark RPCs that should be tracked.
type ctxKeyTrackRPC struct{}

// muxStatsHandler implements grpc/stats.Handler.  It filters events to the
// Stream RPC only (ignoring Control) and accumulates them into metrics.
// idleNotify receives a signal (non-blocking) each time NumStreams drops to 0.
type muxStatsHandler struct {
	metrics    SessionMetrics
	idleNotify chan struct{}
}

func newMuxStatsHandler() *muxStatsHandler {
	return &muxStatsHandler{idleNotify: make(chan struct{}, 1)}
}

// TagRPC marks the context for Stream RPCs so HandleRPC can filter quickly.
func (h *muxStatsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	if info.FullMethodName == muxpb.Mux_Stream_FullMethodName {
		return context.WithValue(ctx, ctxKeyTrackRPC{}, struct{}{})
	}
	return ctx
}

// HandleRPC updates metrics for tracked (Stream) RPCs.
func (h *muxStatsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	if ctx.Value(ctxKeyTrackRPC{}) == nil {
		return
	}
	switch v := s.(type) {
	case *stats.Begin:
		_ = v
		h.metrics.NumStreams.Add(1)
	case *stats.End:
		if v.Error == nil {
			h.metrics.StreamsSucceeded.Add(1)
		} else {
			h.metrics.StreamsFailed.Add(1)
		}
		if h.metrics.NumStreams.Add(-1) == 0 {
			select {
			case h.idleNotify <- struct{}{}:
			default:
			}
		}
	case *stats.InPayload:
		h.metrics.BytesReceived.Add(uint64(v.Length))
		h.metrics.WireLengthReceived.Add(uint64(v.WireLength))
	case *stats.OutPayload:
		h.metrics.BytesSent.Add(uint64(v.Length))
		h.metrics.WireLengthSent.Add(uint64(v.WireLength))
	}
}

// TagConn is a no-op; connection-level tagging is not needed.
func (h *muxStatsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

// HandleConn is a no-op; connection events are tracked elsewhere.
func (h *muxStatsHandler) HandleConn(context.Context, stats.ConnStats) {}
