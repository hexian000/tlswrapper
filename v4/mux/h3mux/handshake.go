// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// handshakeMsg is the single-round-trip identity exchange over the control stream.
// It is encoded as 4-byte big-endian length prefix followed by JSON.
type handshakeMsg struct {
	Identity      string `json:"identity,omitempty"`
	RejectInbound bool   `json:"reject_inbound,omitempty"`
}

const maxHandshakeMsgSize = 4096

// writeHandshake encodes and writes a handshake message to w.
func writeHandshake(w io.Writer, msg handshakeMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: json encode: %v", ErrHandshakeFailed, err)
	}
	if len(data) > maxHandshakeMsgSize {
		return fmt.Errorf("%w: message too large (%d bytes)", ErrHandshakeFailed, len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err = w.Write(hdr[:]); err != nil {
		return fmt.Errorf("%w: write header: %v", ErrHandshakeFailed, err)
	}
	if _, err = w.Write(data); err != nil {
		return fmt.Errorf("%w: write body: %v", ErrHandshakeFailed, err)
	}
	return nil
}

// readHandshake reads and decodes a handshake message from r.
func readHandshake(r io.Reader) (handshakeMsg, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return handshakeMsg{}, fmt.Errorf("%w: read header: %v", ErrHandshakeFailed, err)
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxHandshakeMsgSize {
		return handshakeMsg{}, fmt.Errorf("%w: message too large (%d bytes)", ErrHandshakeFailed, size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return handshakeMsg{}, fmt.Errorf("%w: read body: %v", ErrHandshakeFailed, err)
	}
	var msg handshakeMsg
	if err := json.Unmarshal(buf, &msg); err != nil {
		return handshakeMsg{}, fmt.Errorf("%w: json decode: %v", ErrHandshakeFailed, err)
	}
	return msg, nil
}

// doClientHandshake performs the client side of the h3mux handshake over ctrl.
// It sends a ClientHello then waits for a ServerHello.
// Returns the peer's identity and whether the peer rejects inbound streams.
func doClientHandshake(ctrl io.ReadWriter, localID string, rejectInbound bool) (string, bool, error) {
	// Send ClientHello
	hello := handshakeMsg{Identity: localID, RejectInbound: rejectInbound}
	if err := writeHandshake(ctrl, hello); err != nil {
		return "", false, err
	}
	// Receive ServerHello
	reply, err := readHandshake(ctrl)
	if err != nil {
		return "", false, err
	}
	return reply.Identity, reply.RejectInbound, nil
}

// doServerHandshake performs the server side of the h3mux handshake over ctrl.
// It waits for a ClientHello then sends a ServerHello.
// Returns the peer's identity and whether the peer rejects inbound streams.
func doServerHandshake(ctrl io.ReadWriter, localID string, rejectInbound bool) (string, bool, error) {
	// Receive ClientHello
	hello, err := readHandshake(ctrl)
	if err != nil {
		return "", false, err
	}
	// Send ServerHello
	reply := handshakeMsg{Identity: localID, RejectInbound: rejectInbound}
	if err := writeHandshake(ctrl, reply); err != nil {
		return "", false, err
	}
	return hello.Identity, hello.RejectInbound, nil
}
