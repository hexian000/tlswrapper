// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"fmt"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// controlStream abstracts both client-side and server-side Control bidi streams.
type controlStream interface {
	Send(*muxpb.ControlMessage) error
	Recv() (*muxpb.ControlMessage, error)
}

// doClientHandshake sends ClientHello and waits for ServerHello on the control stream.
// Returns the peer identity, inbound rejection, and optional lossymux offer.
func doClientHandshake(ctrl controlStream, localID string, rejectInbound, lossyMux bool) (peerIdentity string, peerRejectsInbound, peerLossyMux bool, err error) {
	if err = ctrl.Send(&muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_ClientHello{
			ClientHello: &muxpb.ClientHello{
				Identity:      localID,
				RejectInbound: rejectInbound,
				Extensions:    offeredExtensions(lossyMux),
			},
		},
	}); err != nil {
		return
	}

	msg, err := ctrl.Recv()
	if err != nil {
		return
	}
	ack, ok := msg.Body.(*muxpb.ControlMessage_ServerHello)
	if !ok {
		err = fmt.Errorf("%w: expected ServerHello, got %T", errUnexpectedMessage, msg.Body)
		return
	}
	peerIdentity = ack.ServerHello.GetIdentity()
	peerRejectsInbound = ack.ServerHello.GetRejectInbound()
	peerLossyMux = ack.ServerHello.GetExtensions().GetLossymux()
	return
}

// doServerHandshake waits for ClientHello and replies with ServerHello on the control stream.
// Returns the peer identity, inbound rejection, and optional lossymux offer.
func doServerHandshake(ctrl controlStream, localID string, rejectInbound, lossyMux bool) (peerIdentity string, peerRejectsInbound, peerLossyMux bool, err error) {
	msg, err := ctrl.Recv()
	if err != nil {
		return
	}
	hello, ok := msg.Body.(*muxpb.ControlMessage_ClientHello)
	if !ok {
		err = fmt.Errorf("%w: expected ClientHello, got %T", errUnexpectedMessage, msg.Body)
		return
	}
	peerIdentity = hello.ClientHello.GetIdentity()
	peerRejectsInbound = hello.ClientHello.GetRejectInbound()
	peerLossyMux = hello.ClientHello.GetExtensions().GetLossymux()

	err = ctrl.Send(&muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_ServerHello{
			ServerHello: &muxpb.ServerHello{
				Identity:      localID,
				RejectInbound: rejectInbound,
				Extensions:    offeredExtensions(lossyMux),
			},
		},
	})
	return
}

func offeredExtensions(enabled bool) *muxpb.Extensions {
	if !enabled {
		return nil
	}
	return &muxpb.Extensions{Lossymux: true}
}
