// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"errors"
	"io"
	"testing"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// mockControlStream is a deterministic in-memory controlStream for testing handshakes.
// Recv returns msgs in order, then recvErr (default io.EOF).
// Send appends to sent, or returns sendErr immediately.
type mockControlStream struct {
	msgs    []*muxpb.ControlMessage
	msgIdx  int
	recvErr error

	sent    []*muxpb.ControlMessage
	sendErr error
}

func (m *mockControlStream) Send(msg *muxpb.ControlMessage) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockControlStream) Recv() (*muxpb.ControlMessage, error) {
	if m.msgIdx < len(m.msgs) {
		msg := m.msgs[m.msgIdx]
		m.msgIdx++
		return msg, nil
	}
	if m.recvErr != nil {
		return nil, m.recvErr
	}
	return nil, io.EOF
}

func clientHelloMsg(identity string, rejectInbound bool) *muxpb.ControlMessage {
	return &muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_ClientHello{
			ClientHello: &muxpb.ClientHello{
				Identity:      identity,
				RejectInbound: rejectInbound,
			},
		},
	}
}

func serverHelloMsg(identity string, rejectInbound bool) *muxpb.ControlMessage {
	return &muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_ServerHello{
			ServerHello: &muxpb.ServerHello{
				Identity:      identity,
				RejectInbound: rejectInbound,
			},
		},
	}
}

func TestDoClientHandshake(t *testing.T) {
	t.Run("happy-path", func(t *testing.T) {
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{serverHelloMsg("srv", false)},
		}
		peerIdentity, reject, err := doClientHandshake(ms, "cli", false)
		if err != nil {
			t.Fatal(err)
		}
		if peerIdentity != "srv" {
			t.Fatalf("peerIdentity = %q, want %q", peerIdentity, "srv")
		}
		if reject {
			t.Fatal("rejectInbound should be false")
		}
		// Verify that ClientHello was sent with the correct local ID.
		if len(ms.sent) != 1 {
			t.Fatalf("sent %d messages, want 1", len(ms.sent))
		}
		hello, ok := ms.sent[0].Body.(*muxpb.ControlMessage_ClientHello)
		if !ok {
			t.Fatal("sent message is not ClientHello")
		}
		if hello.ClientHello.GetIdentity() != "cli" {
			t.Fatalf("sent identity = %q, want %q", hello.ClientHello.GetIdentity(), "cli")
		}
	})

	t.Run("reject-inbound-propagated", func(t *testing.T) {
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{serverHelloMsg("srv", true)},
		}
		_, reject, err := doClientHandshake(ms, "cli", false)
		if err != nil {
			t.Fatal(err)
		}
		if !reject {
			t.Fatal("expected rejectInbound=true from peer")
		}
	})

	t.Run("local-reject-inbound-sent", func(t *testing.T) {
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{serverHelloMsg("srv", false)},
		}
		_, _, err := doClientHandshake(ms, "cli", true)
		if err != nil {
			t.Fatal(err)
		}
		hello, ok := ms.sent[0].Body.(*muxpb.ControlMessage_ClientHello)
		if !ok {
			t.Fatal("sent message is not ClientHello")
		}
		if !hello.ClientHello.GetRejectInbound() {
			t.Fatal("expected RejectInbound=true in sent ClientHello")
		}
	})

	t.Run("send-error", func(t *testing.T) {
		ms := &mockControlStream{sendErr: io.ErrClosedPipe}
		_, _, err := doClientHandshake(ms, "cli", false)
		if err == nil {
			t.Fatal("expected error when Send fails")
		}
	})

	t.Run("recv-error", func(t *testing.T) {
		ms := &mockControlStream{recvErr: io.ErrUnexpectedEOF}
		_, _, err := doClientHandshake(ms, "cli", false)
		if err == nil {
			t.Fatal("expected error when Recv fails")
		}
	})

	t.Run("wrong-message-type", func(t *testing.T) {
		// Server sends a ClientHello instead of the expected ServerHello.
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{clientHelloMsg("other", false)},
		}
		_, _, err := doClientHandshake(ms, "cli", false)
		if !errors.Is(err, errUnexpectedMessage) {
			t.Fatalf("got %v, want errUnexpectedMessage", err)
		}
	})
}

func TestDoServerHandshake(t *testing.T) {
	t.Run("happy-path", func(t *testing.T) {
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{clientHelloMsg("cli", false)},
		}
		peerIdentity, reject, err := doServerHandshake(ms, "srv", false)
		if err != nil {
			t.Fatal(err)
		}
		if peerIdentity != "cli" {
			t.Fatalf("peerIdentity = %q, want %q", peerIdentity, "cli")
		}
		if reject {
			t.Fatal("rejectInbound should be false")
		}
		// Verify that ServerHello was sent with the correct local ID.
		if len(ms.sent) != 1 {
			t.Fatalf("sent %d messages, want 1", len(ms.sent))
		}
		hello, ok := ms.sent[0].Body.(*muxpb.ControlMessage_ServerHello)
		if !ok {
			t.Fatal("sent message is not ServerHello")
		}
		if hello.ServerHello.GetIdentity() != "srv" {
			t.Fatalf("sent identity = %q, want %q", hello.ServerHello.GetIdentity(), "srv")
		}
	})

	t.Run("reject-inbound-propagated", func(t *testing.T) {
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{clientHelloMsg("cli", true)},
		}
		_, reject, err := doServerHandshake(ms, "srv", false)
		if err != nil {
			t.Fatal(err)
		}
		if !reject {
			t.Fatal("expected rejectInbound=true from peer")
		}
	})

	t.Run("local-reject-inbound-sent", func(t *testing.T) {
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{clientHelloMsg("cli", false)},
		}
		_, _, err := doServerHandshake(ms, "srv", true)
		if err != nil {
			t.Fatal(err)
		}
		hello, ok := ms.sent[0].Body.(*muxpb.ControlMessage_ServerHello)
		if !ok {
			t.Fatal("sent message is not ServerHello")
		}
		if !hello.ServerHello.GetRejectInbound() {
			t.Fatal("expected RejectInbound=true in sent ServerHello")
		}
	})

	t.Run("recv-error", func(t *testing.T) {
		ms := &mockControlStream{recvErr: io.ErrUnexpectedEOF}
		_, _, err := doServerHandshake(ms, "srv", false)
		if err == nil {
			t.Fatal("expected error when Recv fails")
		}
	})

	t.Run("wrong-message-type", func(t *testing.T) {
		// Client sends a ServerHello instead of the expected ClientHello.
		ms := &mockControlStream{
			msgs: []*muxpb.ControlMessage{serverHelloMsg("other", false)},
		}
		_, _, err := doServerHandshake(ms, "srv", false)
		if !errors.Is(err, errUnexpectedMessage) {
			t.Fatalf("got %v, want errUnexpectedMessage", err)
		}
	})

	t.Run("send-error", func(t *testing.T) {
		ms := &mockControlStream{
			msgs:    []*muxpb.ControlMessage{clientHelloMsg("cli", false)},
			sendErr: io.ErrClosedPipe,
		}
		_, _, err := doServerHandshake(ms, "srv", false)
		if err == nil {
			t.Fatal("expected error when Send fails")
		}
	})
}
