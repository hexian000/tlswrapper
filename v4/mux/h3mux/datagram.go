// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"encoding/binary"
	"errors"

	"github.com/hexian000/tlswrapper/v4/mux"
	"github.com/quic-go/quic-go"
)

// The optional lossymux payload is kind (0=data, 1=close), a network-order
// uint16 flow ID, and the UDP body. Native QUIC datagrams are never fragmented.
func encodeDatagram(v mux.Datagram) []byte {
	p := make([]byte, 3+len(v.Data))
	if v.Close {
		p[0] = 1
	}
	binary.BigEndian.PutUint16(p[1:3], v.FlowID)
	copy(p[3:], v.Data)
	return p
}

func decodeDatagram(p []byte) (mux.Datagram, bool) {
	if len(p) < 3 || len(p) > 65538 || p[0] > 1 || (p[0] == 1 && len(p) != 3) {
		return mux.Datagram{}, false
	}
	v := mux.Datagram{FlowID: binary.BigEndian.Uint16(p[1:3]), Close: p[0] == 1, Data: p[3:]}
	return v, v.FlowID != 0
}

func (s *h3Session) startDatagrams() {
	s.dgrams.Activate()
	go func() {
		defer s.close()
		_ = s.dgrams.SendLoop(s.conn.Context(), func(v mux.Datagram) error {
			err := s.conn.SendDatagram(encodeDatagram(v))
			var tooLarge *quic.DatagramTooLargeError
			if errors.As(err, &tooLarge) {
				return mux.ErrDatagramTooLarge
			}
			return err
		})
	}()
	go func() {
		defer s.close()
		for {
			p, err := s.conn.ReceiveDatagram(s.conn.Context())
			if err != nil {
				return
			}
			if v, ok := decodeDatagram(p); ok {
				s.dgrams.Deliver(v)
			}
		}
	}()
}
