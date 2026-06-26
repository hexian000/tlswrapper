// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import "testing"

func TestDirtyBufferPoolGet(t *testing.T) {
	p := newDirtyBufferPool(256, 4096, 16384)
	tests := []struct {
		length  int
		wantCap int
	}{
		{0, 256},
		{1, 256},
		{256, 256},
		{257, 4096},
		{4096, 4096},
		{16384, 16384},
		{16385, 16385}, // over the largest tier: plain allocation
	}
	for _, tt := range tests {
		buf := p.Get(tt.length)
		if len(*buf) != tt.length {
			t.Fatalf("Get(%d): len = %d, want %d", tt.length, len(*buf), tt.length)
		}
		if cap(*buf) != tt.wantCap {
			t.Fatalf("Get(%d): cap = %d, want %d", tt.length, cap(*buf), tt.wantCap)
		}
		p.Put(buf)
	}
}

func TestDirtyBufferPoolRecyclesByCapacity(t *testing.T) {
	p := newDirtyBufferPool(256, 4096)
	buf := p.Get(4096)
	(*buf)[0] = 0xa5
	// Shrink to a prefix as mem.BufferPool callers are allowed to do; the
	// buffer must return to the tier matching its capacity, not its length.
	*buf = (*buf)[:1]
	p.Put(buf)
	got := p.Get(4096)
	if len(*got) != 4096 {
		t.Fatalf("Get(4096) after Put: len = %d, want 4096", len(*got))
	}
}

func TestDirtyBufferPoolDropsUndersized(t *testing.T) {
	p := newDirtyBufferPool(256)
	b := make([]byte, 16)
	p.Put(&b) // must not panic; buffer is below the smallest tier
	if buf := p.Get(256); len(*buf) != 256 || cap(*buf) < 256 {
		t.Fatalf("Get(256) = len %d cap %d, want len 256 cap >= 256", len(*buf), cap(*buf))
	}
}
