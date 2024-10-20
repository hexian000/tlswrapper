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

	Type = mime.FormatMediaType(mimeType, map[string]string{"version": mimeVersion})
)

const (
	MsgClientHello = iota
	MsgServerHello
)

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
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(b)))
	_, err = w.Write(append(hdr, b...))
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

func Write(w io.Writer, rsp *Message) error {
	return sendmsg(w, rsp)
}
