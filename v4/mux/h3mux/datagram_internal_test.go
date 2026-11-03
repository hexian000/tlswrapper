// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/hexian000/tlswrapper/v4/mux"
	"testing"
)

func TestDatagramEncoding(t *testing.T) {
	for _, v := range []mux.Datagram{{FlowID: 1}, {FlowID: 65535, Data: []byte("payload")}, {FlowID: 2, Close: true}} {
		got, ok := decodeDatagram(encodeDatagram(v))
		if !ok || got.FlowID != v.FlowID || got.Close != v.Close || !bytes.Equal(got.Data, v.Data) {
			t.Fatalf("round trip: %+v", got)
		}
	}
	for _, p := range [][]byte{nil, {0}, {0, 1}, {2, 0, 1}, {0, 0, 0}, {1, 0, 1, 42}, make([]byte, 65539)} {
		if _, ok := decodeDatagram(p); ok {
			t.Fatalf("accepted malformed message: %v", p[:min(len(p), 8)])
		}
	}
}

func TestLossyMuxOptionalHelloExtension(t *testing.T) {
	for _, raw := range []string{`{}`, `{"extensions":{}}`, `{"extensions":{"lossymux":false,"future":true}}`, `{"extensions":{"lossymux":true}}`} {
		b := &bytes.Buffer{}
		if err := binary.Write(b, binary.BigEndian, uint32(len(raw))); err != nil {
			t.Fatal(err)
		}
		b.WriteString(raw)
		msg, err := readHandshake(b)
		if err != nil {
			t.Fatal(err)
		}
		want := raw == `{"extensions":{"lossymux":true}}`
		if msg.Extensions.lossyMux() != want {
			t.Fatalf("wrong offer for %s", raw)
		}
	}
	for _, enabled := range []bool{false, true} {
		p, err := json.Marshal(handshakeMsg{Extensions: offeredExtensions(enabled)})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(p, []byte("extensions")) != enabled {
			t.Fatalf("unsolicited extension: %s", p)
		}
	}
}
