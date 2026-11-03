// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/hexian000/tlswrapper/v4/mux"
	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

type datagramStream interface {
	Send(*muxpb.Datagram) error
	Recv() (*muxpb.Datagram, error)
}

// startDatagrams waits for response headers to ensure the server attached the
// channel. It uses the handshake deadline without shortening the RPC lifetime.
func (s *clientSession) startDatagrams(ctx context.Context) error {
	stream, err := s.grpcClient.Datagrams(s.streamCtx, grpc.MaxCallRecvMsgSize(65535+64))
	if err != nil {
		return err
	}
	ready := make(chan error, 1)
	go func() {
		headers, err := stream.Header()
		if err == nil {
			values := headers.Get("x-mux-lossymux")
			if len(values) != 1 || values[0] != "1" {
				err = fmt.Errorf("lossymux attachment not acknowledged")
			}
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			return fmt.Errorf("lossymux attach: %w", err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	s.dgrams.Activate()
	s.runDatagrams(stream, s.streamCtx, func() { _ = s.Close() })
	return nil
}

// runDatagrams has exactly one writer and reader. The bounded receive handoff
// always drains the RPC even when local UDP delivery is congested.
func (s *session) runDatagrams(stream datagramStream, ctx context.Context, closeSession func()) {
	go func() {
		defer closeSession()
		_ = s.dgrams.SendLoop(ctx, func(v mux.Datagram) error {
			return stream.Send(&muxpb.Datagram{FlowId: uint32(v.FlowID), Data: v.Data, Close: v.Close})
		})
	}()
	go func() {
		defer closeSession()
		for {
			v, err := stream.Recv()
			if err != nil {
				return
			}
			if v.GetFlowId() > 65535 {
				continue
			}
			s.dgrams.Deliver(mux.Datagram{FlowID: uint16(v.GetFlowId()), Data: v.GetData(), Close: v.GetClose()})
		}
	}()
}

// Datagrams never creates a session. Control must have completed its optional
// extension negotiation before this RPC can attach, and duplicates are rejected.
func (svc *muxServer) Datagrams(stream muxpb.Mux_DatagramsServer) error {
	svc.mu.RLock()
	started := svc.ctrlStarted
	svc.mu.RUnlock()
	if !started {
		return status.Error(codes.FailedPrecondition, "Control required")
	}
	select {
	case <-svc.sessReady:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	svc.mu.Lock()
	sess := svc.sess
	if sess == nil || sess.dgrams == nil || sess.IsClosed() {
		svc.mu.Unlock()
		return status.Error(codes.FailedPrecondition, "lossymux not negotiated")
	}
	if svc.datagramsStarted {
		svc.mu.Unlock()
		return status.Error(codes.AlreadyExists, "lossymux already attached")
	}
	svc.datagramsStarted = true
	sess.dgrams.Activate()
	svc.mu.Unlock()
	if err := stream.SendHeader(metadata.Pairs("x-mux-lossymux", "1")); err != nil {
		_ = sess.Close()
		return err
	}
	sess.runDatagrams(stream, stream.Context(), func() { _ = sess.Close() })
	select {
	case <-sess.CloseChan():
	case <-stream.Context().Done():
		_ = sess.Close()
	}
	return nil
}
