package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
)

var (
	versionStr = "3"

	mimeType   = "application/x-tlswrapper-msg"
	mimeParams = map[string]string{"version": versionStr}

	Type = mime.FormatMediaType(mimeType, mimeParams)
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

func sendmsg(conn net.Conn, msg interface{}) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(b) > math.MaxUint16 {
		return ErrMsgTooLong
	}
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(b)))
	_, err = conn.Write(append(hdr, b...))
	return err
}

func recvmsg(conn net.Conn, msg interface{}) error {
	hdr := make([]byte, 2)
	_, err := io.ReadFull(conn, hdr)
	if err != nil {
		return err
	}
	b := make([]byte, int(binary.BigEndian.Uint16(hdr)))
	_, err = io.ReadFull(conn, b)
	if err != nil {
		return err
	}
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
	if version != versionStr {
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
		return nil, err
	}
	return rsp, nil
}

func RecvMessage(conn net.Conn) (*Message, error) {
	req := &Message{}
	if err := recvmsg(conn, req); err != nil {
		return nil, err
	}
	if err := checkType(req.Type); err != nil {
		return nil, err
	}
	return req, nil
}

func SendMessage(conn net.Conn, rsp *Message) error {
	return sendmsg(conn, rsp)
}
