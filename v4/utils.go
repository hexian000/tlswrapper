// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
)

// ioClose logs close failures instead of dropping them.
func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Println(2, slog.LevelWarning, nil, msg)
	}
}

type tcpConn interface {
	SetNoDelay(bool) error
	SetKeepAlive(bool) error
	SetReadBuffer(bytes int) error
	SetWriteBuffer(bytes int) error
}

// setTCPConnParams applies configured socket options when conn exposes them.
func setTCPConnParams(tcp config.TCP, conn net.Conn) {
	tcpConn, ok := conn.(tcpConn)
	if !ok {
		return
	}
	if err := tcpConn.SetNoDelay(tcp.NoDelay); err != nil {
		slog.Warningf("SetNoDelay: %s", formats.Error(err))
	}
	if err := tcpConn.SetKeepAlive(tcp.KeepAlive); err != nil {
		slog.Warningf("SetKeepAlive: %s", formats.Error(err))
	}
	if tcp.ReadBuffer > 0 {
		if err := tcpConn.SetReadBuffer(tcp.ReadBuffer); err != nil {
			slog.Warningf("SetReadBuffer %d: %s", tcp.ReadBuffer, formats.Error(err))
		}
	}
	if tcp.WriteBuffer > 0 {
		if err := tcpConn.SetWriteBuffer(tcp.WriteBuffer); err != nil {
			slog.Warningf("SetWriteBuffer %d: %s", tcp.WriteBuffer, formats.Error(err))
		}
	}
}

const latencyRingSize = 256

// latencyRing is a fixed-size circular buffer of duration samples.
// It is safe for concurrent use.
type latencyRing struct {
	mu  sync.Mutex
	buf [latencyRingSize]int64 // nanoseconds
	pos int
	n   int
}

// Record appends d to the ring, overwriting the oldest entry when full.
func (r *latencyRing) Record(d time.Duration) {
	r.mu.Lock()
	r.buf[r.pos] = d.Nanoseconds()
	r.pos = (r.pos + 1) % latencyRingSize
	if r.n < latencyRingSize {
		r.n++
	}
	r.mu.Unlock()
}

// Snapshot returns a fixed-size array of the current ring contents as
// time.Duration values. Only the first n entries (where n == r.n) are valid;
// the remaining slots are zero.
func (r *latencyRing) Snapshot() (out [latencyRingSize]time.Duration) {
	r.mu.Lock()
	n := r.n
	for i := 0; i < n; i++ {
		out[i] = time.Duration(r.buf[i])
	}
	r.mu.Unlock()
	return
}

// Percentiles returns P50/P90/P99/MAX over the buffered samples.
// ok is false when no samples have been recorded yet.
func (r *latencyRing) Percentiles() (p50, p90, p99, pmax time.Duration, ok bool) {
	r.mu.Lock()
	n := r.n
	if n == 0 {
		r.mu.Unlock()
		return
	}
	tmp := make([]int64, n)
	copy(tmp, r.buf[:n])
	r.mu.Unlock()
	slices.Sort(tmp)
	at := func(pct float64) time.Duration {
		i := int(math.Floor(float64(n) * pct))
		if i >= n {
			i = n - 1
		}
		return time.Duration(tmp[i])
	}
	ok = true
	p50 = at(0.50)
	p90 = at(0.90)
	p99 = at(0.99)
	pmax = time.Duration(tmp[n-1])
	return
}
