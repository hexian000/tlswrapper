// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"fmt"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/proto"
)

// controlStream abstracts both client-side and server-side Control bidi streams.
type controlStream interface {
	Send(*muxpb.ControlMessage) error
	Recv() (*muxpb.ControlMessage, error)
}

// doClientHandshake sends ClientHello and waits for ServerHello on the control stream.
// Returns peerID and peerRejectsInbound parsed from the ServerHello.
func doClientHandshake(ctrl controlStream, localID string, rejectInbound bool) (peerID string, peerRejectsInbound bool, err error) {
	if err = ctrl.Send(&muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_ClientHello{
			ClientHello: &muxpb.ClientHello{
				Identity:      localID,
				RejectInbound: rejectInbound,
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
	peerID = ack.ServerHello.GetIdentity()
	peerRejectsInbound = ack.ServerHello.GetRejectInbound()
	return
}

// doServerHandshake waits for ClientHello and replies with ServerHello on the control stream.
// Returns peerID and peerRejectsInbound parsed from the ClientHello.
func doServerHandshake(ctrl controlStream, localID string, rejectInbound bool) (peerID string, peerRejectsInbound bool, err error) {
	msg, err := ctrl.Recv()
	if err != nil {
		return
	}
	hello, ok := msg.Body.(*muxpb.ControlMessage_ClientHello)
	if !ok {
		err = fmt.Errorf("%w: expected ClientHello, got %T", errUnexpectedMessage, msg.Body)
		return
	}
	peerID = hello.ClientHello.GetIdentity()
	peerRejectsInbound = hello.ClientHello.GetRejectInbound()

	err = ctrl.Send(&muxpb.ControlMessage{
		Body: &muxpb.ControlMessage_ServerHello{
			ServerHello: &muxpb.ServerHello{
				Identity:      localID,
				RejectInbound: rejectInbound,
			},
		},
	})
	return
}
