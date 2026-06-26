// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"bytes"
	"testing"

	"google.golang.org/grpc/encoding"
	grpcproto "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

func testCodec(t *testing.T) encoding.CodecV2 {
	t.Helper()
	c := encoding.GetCodecV2(grpcproto.Name)
	if _, ok := c.(rawChunkCodec); !ok {
		t.Fatalf("registered %q codec is %T, want rawChunkCodec", grpcproto.Name, c)
	}
	return c
}

func (rc *rawChunk) materialize() []byte {
	return rc.bufs.Materialize()
}

func TestRawChunkCodecMarshalMatchesProto(t *testing.T) {
	c := testCodec(t)
	for _, payload := range [][]byte{nil, {}, []byte("x"), bytes.Repeat([]byte("data"), 8192)} {
		got, err := c.Marshal(&rawChunk{payload: payload})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		want, err := proto.Marshal(&muxpb.Chunk{Data: payload})
		if err != nil {
			t.Fatalf("proto.Marshal() error = %v", err)
		}
		if !bytes.Equal(got.Materialize(), want) {
			t.Fatalf("Marshal(%d bytes) wire mismatch: got %d bytes, want %d bytes",
				len(payload), got.Len(), len(want))
		}
		got.Free()
	}
}

func TestRawChunkCodecUnmarshalFastPath(t *testing.T) {
	c := testCodec(t)
	payload := bytes.Repeat([]byte("payload."), 4096) // 32 KiB
	wire, err := proto.Marshal(&muxpb.Chunk{Data: payload})
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}
	// Split the wire bytes into multiple buffers like multi-frame receives do.
	for _, splitAt := range []int{len(wire), 16384, 5} {
		var data mem.BufferSlice
		for rest := wire; len(rest) > 0; {
			n := min(splitAt, len(rest))
			data = append(data, mem.SliceBuffer(rest[:n]))
			rest = rest[n:]
		}
		var rc rawChunk
		if err := c.Unmarshal(data, &rc); err != nil {
			t.Fatalf("Unmarshal(split %d) error = %v", splitAt, err)
		}
		if got := rc.materialize(); !bytes.Equal(got, payload) {
			t.Fatalf("Unmarshal(split %d) = %d bytes, want %d bytes", splitAt, len(got), len(payload))
		}
		rc.bufs.Free()
	}
}

func TestRawChunkCodecUnmarshalEmpty(t *testing.T) {
	c := testCodec(t)
	var rc rawChunk
	if err := c.Unmarshal(mem.BufferSlice{}, &rc); err != nil {
		t.Fatalf("Unmarshal(empty) error = %v", err)
	}
	if len(rc.bufs) != 0 {
		t.Fatalf("Unmarshal(empty) bufs = %d, want 0", len(rc.bufs))
	}
}

func TestRawChunkCodecUnmarshalUnknownField(t *testing.T) {
	c := testCodec(t)
	payload := []byte("payload")
	wire, err := proto.Marshal(&muxpb.Chunk{Data: payload})
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}
	// Append an unknown varint field (field 2) to force the slow path.
	wire = append(wire, 0x10, 0x01)
	var rc rawChunk
	if err := c.Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, &rc); err != nil {
		t.Fatalf("Unmarshal(unknown field) error = %v", err)
	}
	if got := rc.materialize(); !bytes.Equal(got, payload) {
		t.Fatalf("Unmarshal(unknown field) = %q, want %q", got, payload)
	}
}

func TestRawChunkCodecDelegates(t *testing.T) {
	c := testCodec(t)
	msg := &muxpb.Chunk{Data: []byte("delegated")}
	data, err := c.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal(proto message) error = %v", err)
	}
	defer data.Free()
	var got muxpb.Chunk
	if err := c.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(proto message) error = %v", err)
	}
	if !bytes.Equal(got.Data, msg.Data) {
		t.Fatalf("round trip = %q, want %q", got.Data, msg.Data)
	}
}
