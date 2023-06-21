package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
)

type Handshake struct {
	Identity string `json:"identity"`
}

func RunHandshake(conn net.Conn, p *Handshake) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(b) > math.MaxUint16 {
		return errors.New("handshake message too long")
	}
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(b)))
	_, err = conn.Write(hdr)
	if err != nil {
		return err
	}
	_, err = conn.Write(b)
	if err != nil {
		return err
	}
	_, err = io.ReadFull(conn, hdr)
	if err != nil {
		return err
	}
	b = make([]byte, int(binary.BigEndian.Uint16(hdr)))
	_, err = io.ReadFull(conn, b)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, p)
}
