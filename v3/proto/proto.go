package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
)

const Type = "application/x-tlswrapper; version=3"

type ClientHello struct {
	Type    string `json:"type"`
	Service string `json:"service"`
}

type ServerHello struct {
	Type    string `json:"type"`
	Service string `json:"service"`
}

var (
	ErrMsgTooLong          = errors.New("message too long")
	ErrUnsupportedProtocol = errors.New("unsupported protocol")
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

func Client(conn net.Conn, req *ClientHello) (*ServerHello, error) {
	if err := sendmsg(conn, req); err != nil {
		return nil, err
	}
	rsp := &ServerHello{}
	if err := recvmsg(conn, rsp); err != nil {
		return nil, err
	}
	if rsp.Type != Type {
		return nil, ErrUnsupportedProtocol
	}
	return rsp, nil
}

func Server(conn net.Conn, rsp *ServerHello) (*ClientHello, error) {
	req := &ClientHello{}
	if err := recvmsg(conn, req); err != nil {
		return nil, err
	}
	if req.Type != Type {
		return nil, ErrUnsupportedProtocol
	}
	if err := sendmsg(conn, rsp); err != nil {
		return nil, err
	}
	return req, nil
}
