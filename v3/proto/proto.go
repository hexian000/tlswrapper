// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"

	"github.com/hexian000/gosnippets/slog"
)

var (
	mimeType    = "application/x-tlswrapper-proto"
	mimeVersion = "3"

	// Type is the protocol message type identifier in MIME format
	Type = mime.FormatMediaType(mimeType, map[string]string{"version": mimeVersion})
)

const (
	MsgClientHello = iota
	MsgServerHello
)

// Message represents a protocol message
type Message struct {
	Type     string `json:"type"`
	Msg      int    `json:"msgid"`
	PeerName string `json:"peername,omitempty"`
	Service  string `json:"service,omitempty"`
}

var (
	ErrMsgTooLong           = errors.New("message too long")
	ErrUnsupportedProtocol  = errors.New("unsupported protocol")
	ErrIncompatiableVersion = errors.New("incompatible protocol version")
)

func sendmsg(w io.Writer, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(b) > math.MaxUint16 {
		return ErrMsgTooLong
	}
	pkt := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(pkt[:2], uint16(len(b)))
	_ = copy(pkt[2:], b)
	_, err = w.Write(pkt)
	return err
}

func recvmsg(r io.Reader, msg any) error {
	hdr := make([]byte, 2)
	_, err := io.ReadFull(r, hdr)
	if err != nil {
		return err
	}
	b := make([]byte, int(binary.BigEndian.Uint16(hdr)))
	_, err = io.ReadFull(r, b)
	if err != nil {
		return err
	}
	slog.Binaryf(slog.LevelVeryVerbose, b, "recvmsg: %d bytes", len(b))
	if err := json.Unmarshal(b, msg); err != nil {
		return err
	}
	return nil
}

func checkType(s string) error {
	mediatype, params, err := mime.ParseMediaType(s)
	if err != nil {
		return err
	}
	if mediatype != mimeType {
		return ErrUnsupportedProtocol
	}
	version, ok := params["version"]
	if !ok {
		return ErrUnsupportedProtocol
	}
	if version != mimeVersion {
		return ErrIncompatiableVersion
	}
	return nil
}

// Roundtrip sends a request message and waits for the response
func Roundtrip(conn net.Conn, req *Message) (*Message, error) {
	if err := sendmsg(conn, req); err != nil {
		return nil, err
	}
	rsp := &Message{}
	if err := recvmsg(conn, rsp); err != nil {
		return nil, err
	}
	if err := checkType(rsp.Type); err != nil {
		slog.Verbosef("type: %q", rsp.Type)
		return nil, err
	}
	return rsp, nil
}

// Read reads a Message from the given reader
func Read(r io.Reader) (*Message, error) {
	req := &Message{}
	if err := recvmsg(r, req); err != nil {
		return nil, err
	}
	if err := checkType(req.Type); err != nil {
		slog.Verbosef("type: %q", req.Type)
		return nil, err
	}
	return req, nil
}

// Write writes a Message to the given writer
func Write(w io.Writer, rsp *Message) error {
	return sendmsg(w, rsp)
}
