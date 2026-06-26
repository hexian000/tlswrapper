// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package forwarder

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/routines"
	"github.com/hexian000/gosnippets/slog"
)

// ErrConnLimit is returned when the maximum number of concurrent connections is exceeded
var ErrConnLimit = errors.New("connection limit is exceeded")

// copyBufPool pools buffers reused across io.CopyBuffer calls.
// Each buffer becomes one h2mux chunk. Larger chunks amortize per-message
// costs (proto encode/decode, channel wakeups, allocations); 64 KiB measured
// faster than 16 KiB (one HTTP/2 frame) despite the receive-side merge copy
// that multi-frame messages incur.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

// DrainPool discards one pooled buffer so the GC can reclaim it over time.
// Intended for periodic background calls to slowly shrink the pool under low load.
func DrainPool() { _ = copyBufPool.Get() }

// CloseWriter is implemented by connections that support a half-close (write-side shutdown),
// such as *net.TCPConn or mux client streams.
type CloseWriter interface {
	CloseWrite() error
}

// EventHandler receives notifications from a forwarded connection pair.
type EventHandler interface {
	// OnWriteClosed is called when one copy direction finishes.
	// conn is the source connection of that direction (the side that sent EOF or the error).
	// err is the raw error returned by io.Copy; nil means clean EOF.
	// Called exactly twice, once per direction; may be called concurrently.
	OnWriteClosed(conn net.Conn, err error)
	// OnClosed is called once both copy directions have finished and all resources are released.
	// Called exactly once, from a single goroutine.
	OnClosed()
}

// HandlerFuncs is a convenience adapter that implements EventHandler.
// Nil function fields are safely ignored.
type HandlerFuncs struct {
	WriteClosed func(net.Conn, error)
	Closed      func()
}

func (h HandlerFuncs) OnWriteClosed(conn net.Conn, err error) {
	if h.WriteClosed != nil {
		h.WriteClosed(conn, err)
	}
}

func (h HandlerFuncs) OnClosed() {
	if h.Closed != nil {
		h.Closed()
	}
}

// Forwarder manages bidirectional forwarding between connection pairs.
type Forwarder interface {
	// Start begins forwarding data between accepted and dialed.
	// handler may be nil. If Start returns nil, OnWriteClosed runs once per
	// direction and OnClosed runs once after both directions finish.
	Start(accepted net.Conn, dialed net.Conn, handler EventHandler) error
	// SetLimit adjusts the maximum number of active connection pairs.
	// Pairs already running are unaffected when the limit shrinks.
	SetLimit(maxConn int)
	Count() int
	HalfOpenCount() int
	Close()
}

type forwarder struct {
	mu          sync.Mutex
	g           routines.Group
	conn        map[net.Conn]struct{}
	count       atomic.Int64
	limit       atomic.Int64
	numHalfOpen atomic.Int32
}

// New returns a Forwarder limited to maxConn active connection pairs.
func New(maxConn int, g routines.Group) Forwarder {
	f := &forwarder{
		conn: make(map[net.Conn]struct{}),
		g:    g,
	}
	f.limit.Store(int64(maxConn))
	return f
}

func (f *forwarder) SetLimit(maxConn int) {
	f.limit.Store(int64(maxConn))
}

func (f *forwarder) addConn(accepted net.Conn, dialed net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conn[accepted] = struct{}{}
	f.conn[dialed] = struct{}{}
}

func (f *forwarder) cleanupConn(accepted net.Conn, dialed net.Conn) {
	if err := accepted.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Warningf("close: %s", formats.Error(err))
	}
	if err := dialed.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Warningf("close: %s", formats.Error(err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conn, accepted)
	delete(f.conn, dialed)
}

// connCopy copies from src to dst and returns the io.CopyBuffer error (nil on clean EOF).
func (f *forwarder) connCopy(dst net.Conn, src net.Conn) error {
	defer func() {
		if r := recover(); r != nil {
			slog.Stackf(slog.LevelError, 0, "panic: %v", r)
		}
	}()
	bp := copyBufPool.Get().(*[]byte)
	// Hide the TCP side's WriterTo/ReadFrom so io.CopyBuffer actually uses
	// our buffer: one side is always a mux stream (splice never applies),
	// and the generic fallbacks copy in 32 KiB pieces, which split each mux
	// chunk across two HTTP/2 frames and force a merge copy on receive.
	_, err := io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *bp)
	copyBufPool.Put(bp)
	if err != nil &&
		!errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, syscall.EPIPE) {
		slog.Warningf("stream error: %s", formats.Error(err))
	}
	return err
}

// Start begins forwarding data between accepted and dialed.
func (f *forwarder) Start(accepted net.Conn, dialed net.Conn, handler EventHandler) error {
	select {
	case <-f.g.CloseC():
		return routines.ErrClosed
	default:
	}
	if f.count.Add(1) > f.limit.Load() {
		f.count.Add(-1)
		return ErrConnLimit
	}
	f.addConn(accepted, dialed)
	cleanup := func() {
		f.cleanupConn(accepted, dialed)
		f.count.Add(-1)
	}
	var remaining atomic.Int32
	remaining.Store(2)
	closeOnce := &sync.Once{}
	run := func(dst, src net.Conn) {
		err := f.connCopy(dst, src)
		// On clean EOF, attempt a half-close so the peer can drain remaining data.
		// On error or if half-close is not supported, force-close both connections.
		if err == nil {
			if cw, ok := dst.(CloseWriter); ok {
				_ = cw.CloseWrite()
			} else {
				closeOnce.Do(func() {
					_ = accepted.Close()
					_ = dialed.Close()
				})
			}
		} else {
			closeOnce.Do(func() {
				_ = accepted.Close()
				_ = dialed.Close()
			})
		}
		if handler != nil {
			handler.OnWriteClosed(src, err)
		}
		switch remaining.Add(-1) {
		case 1:
			f.numHalfOpen.Add(1)
		case 0:
			f.numHalfOpen.Add(-1)
			cleanup()
			if handler != nil {
				handler.OnClosed()
			}
		}
	}
	if err := f.g.Go(func() { run(accepted, dialed) }); err != nil {
		cleanup()
		return err
	}
	if err := f.g.Go(func() { run(dialed, accepted) }); err != nil {
		// Signal the first goroutine to stop.
		closeOnce.Do(func() {
			_ = accepted.Close()
			_ = dialed.Close()
		})
		// The second direction never ran; synthesise its OnWriteClosed callback
		// so the handler always receives exactly two calls.
		if handler != nil {
			handler.OnWriteClosed(accepted, err)
		}
		switch remaining.Add(-1) {
		case 1:
			f.numHalfOpen.Add(1)
		case 0:
			f.numHalfOpen.Add(-1)
			// First goroutine already finished; we are responsible for cleanup.
			cleanup()
			if handler != nil {
				handler.OnClosed()
			}
		}
		return err
	}
	return nil
}

func (f *forwarder) Count() int {
	return int(f.count.Load())
}

func (f *forwarder) HalfOpenCount() int {
	return int(f.numHalfOpen.Load())
}

func (f *forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for conn := range f.conn {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warningf("close: %s", formats.Error(err))
		}
	}
}
