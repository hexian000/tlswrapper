// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"sync"

	"google.golang.org/grpc/experimental"
)

// Replace gRPC's default buffer pool with a non-zeroing variant. The default
// pool clears each buffer's full capacity on every Get as defense-in-depth
// against stale data leaking across RPCs; under bulk transfer that clear
// dominates the CPU profile. Every buffer here only ever carries opaque
// tunneled payload, so recycled bytes can never leak anything the peer did
// not already send through the same process.
//
// SetDefaultBufferPool also covers the proto codec, whose pool cannot be
// configured per client/server.
func init() {
	experimental.SetDefaultBufferPool(newDirtyBufferPool(
		256,
		4096,  // Go page size
		16384, // max HTTP/2 frame size used by gRPC
		32768, // default buffer size for io.Copy
		1<<20, // 1 MiB
	))
}

// dirtyBufferPool is a tiered mem.BufferPool that does not zero recycled
// buffers on Get. Tier sizes must be ascending.
type dirtyBufferPool struct {
	sizes []int
	pools []sync.Pool
}

func newDirtyBufferPool(sizes ...int) *dirtyBufferPool {
	p := &dirtyBufferPool{sizes: sizes, pools: make([]sync.Pool, len(sizes))}
	for i, size := range sizes {
		p.pools[i].New = func() any {
			b := make([]byte, size)
			return &b
		}
	}
	return p
}

func (p *dirtyBufferPool) Get(length int) *[]byte {
	for i, size := range p.sizes {
		if length <= size {
			buf := p.pools[i].Get().(*[]byte)
			*buf = (*buf)[:length]
			return buf
		}
	}
	b := make([]byte, length)
	return &b
}

func (p *dirtyBufferPool) Put(buf *[]byte) {
	c := cap(*buf)
	for i := len(p.sizes) - 1; i >= 0; i-- {
		// Pool by capacity so a future Get(length <= sizes[i]) can reslice.
		if c >= p.sizes[i] {
			p.pools[i].Put(buf)
			return
		}
	}
}
