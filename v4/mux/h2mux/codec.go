// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"encoding/binary"

	"google.golang.org/grpc/encoding"
	grpcproto "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protowire"

	muxpb "github.com/hexian000/tlswrapper/v4/mux/h2mux/proto"
)

// rawChunk is a wire-compatible stand-in for muxpb.Chunk on the Stream data
// path. grpcStream passes it to SendMsg/RecvMsg directly so rawChunkCodec can
// bypass protobuf reflection: Marshal copies the payload once into a pooled
// buffer, and Unmarshal hands out references to the transport's receive
// buffers instead of copying them into a fresh allocation per message.
type rawChunk struct {
	// payload is the data to send; read by Marshal.
	payload []byte
	// bufs holds the received data as references into transport buffers;
	// set by Unmarshal. The consumer must Free each buffer once drained.
	bufs mem.BufferSlice
}

// rawChunkField is the field number of muxpb.Chunk.Data.
const rawChunkField = 1

// rawChunkCodec replaces the registered "proto" codec, dispatching *rawChunk
// to the fast path and delegating every other message type (handshake and
// control RPCs) to the original proto codec. The encoded bytes are identical
// to protobuf's, so peers cannot observe the difference.
type rawChunkCodec struct{ base encoding.CodecV2 }

func (c rawChunkCodec) Name() string { return grpcproto.Name }

func (c rawChunkCodec) Marshal(v any) (mem.BufferSlice, error) {
	rc, ok := v.(*rawChunk)
	if !ok {
		return c.base.Marshal(v)
	}
	if len(rc.payload) == 0 {
		// proto3 omits an empty bytes field: the message is empty.
		return mem.BufferSlice{}, nil
	}
	var arr [2 * binary.MaxVarintLen64]byte
	hdr := protowire.AppendTag(arr[:0], rawChunkField, protowire.BytesType)
	hdr = protowire.AppendVarint(hdr, uint64(len(rc.payload)))
	pool := mem.DefaultBufferPool()
	buf := pool.Get(len(hdr) + len(rc.payload))
	n := copy(*buf, hdr)
	copy((*buf)[n:], rc.payload)
	return mem.BufferSlice{mem.NewBuffer(buf, pool)}, nil
}

func (c rawChunkCodec) Unmarshal(data mem.BufferSlice, v any) error {
	rc, ok := v.(*rawChunk)
	if !ok {
		return c.base.Unmarshal(data, v)
	}
	rc.payload = nil
	rc.bufs = nil
	if data.Len() == 0 {
		return nil
	}
	// Expect exactly one bytes field whose value runs to the end of the
	// message, with the field header contiguous in the first buffer. This is
	// the only shape tlswrapper peers produce; anything else (unknown
	// fields, header split across frame buffers) takes the slow path.
	first := data[0].ReadOnlyData()
	num, typ, n := protowire.ConsumeTag(first)
	if n < 0 || num != rawChunkField || typ != protowire.BytesType {
		return c.unmarshalSlow(data, rc)
	}
	size, m := protowire.ConsumeVarint(first[n:])
	if m < 0 {
		return c.unmarshalSlow(data, rc)
	}
	off := n + m
	if off > len(first) || int64(size) != int64(data.Len()-off) {
		return c.unmarshalSlow(data, rc)
	}
	bufs := make(mem.BufferSlice, 0, len(data))
	if rest := data[0].Slice(off, len(first)); rest.Len() > 0 {
		bufs = append(bufs, rest)
	}
	for _, b := range data[1:] {
		b.Ref()
		bufs = append(bufs, b)
	}
	rc.bufs = bufs
	return nil
}

func (c rawChunkCodec) unmarshalSlow(data mem.BufferSlice, rc *rawChunk) error {
	var msg muxpb.Chunk
	if err := c.base.Unmarshal(data, &msg); err != nil {
		return err
	}
	if len(msg.Data) > 0 {
		rc.bufs = mem.BufferSlice{mem.SliceBuffer(msg.Data)}
	}
	return nil
}

func init() {
	base := encoding.GetCodecV2(grpcproto.Name)
	if base == nil {
		panic("h2mux: gRPC proto codec is not registered")
	}
	encoding.RegisterCodecV2(rawChunkCodec{base: base})
}
