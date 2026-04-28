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

// CloseWriter is implemented by connections that support a half-close (write-side shutdown),
// such as *net.TCPConn or mux client streams.
type CloseWriter interface {
	CloseWrite() error
}

// EventHandler receives notifications from a forwarded connection pair.
type EventHandler interface {
	// OnHalfClose is called when one copy direction finishes.
	// conn is the source connection of that direction (the side that sent EOF or the error).
	// err is the raw error returned by io.Copy; nil means clean EOF.
	// Called exactly twice, once per direction; may be called concurrently.
	OnHalfClose(conn net.Conn, err error)
	// OnDone is called once both copy directions have finished and all resources are released.
	// Called exactly once, from a single goroutine.
	OnDone()
}

// HandlerFuncs is a convenience adapter that implements EventHandler.
// Nil function fields are safely ignored.
type HandlerFuncs struct {
	HalfClose func(net.Conn, error)
	Done      func()
}

func (h HandlerFuncs) OnHalfClose(conn net.Conn, err error) {
	if h.HalfClose != nil {
		h.HalfClose(conn, err)
	}
}

func (h HandlerFuncs) OnDone() {
	if h.Done != nil {
		h.Done()
	}
}

// Forwarder manages bidirectional forwarding between connection pairs.
type Forwarder interface {
	// Start begins forwarding data between accepted and dialed.
	// handler may be nil. If Start returns nil, handler.OnDone is called exactly once
	// after both directions finish and resources are released.
	Start(accepted net.Conn, dialed net.Conn, handler EventHandler) error
	Count() int
	Close()
}

type forwarder struct {
	mu      sync.Mutex
	g       routines.Group
	conn    map[net.Conn]struct{}
	counter chan struct{}
}

// New creates a new Forwarder with the given maximum concurrent connections
func New(maxConn int, g routines.Group) Forwarder {
	return &forwarder{
		conn:    make(map[net.Conn]struct{}),
		counter: make(chan struct{}, maxConn),
		g:       g,
	}
}

func (f *forwarder) addConn(accepted net.Conn, dialed net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conn[accepted] = struct{}{}
	f.conn[dialed] = struct{}{}
}

func (f *forwarder) delConn(accepted net.Conn, dialed net.Conn) {
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

// connCopy copies from src to dst and returns the io.Copy error (nil on clean EOF).
func (f *forwarder) connCopy(dst net.Conn, src net.Conn) error {
	defer func() {
		if r := recover(); r != nil {
			slog.Stackf(slog.LevelError, 0, "panic: %v", r)
		}
	}()
	_, err := io.Copy(dst, src)
	if err != nil &&
		!errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, syscall.EPIPE) {
		slog.Warningf("stream error: %s", formats.Error(err))
	}
	return err
}

// Start begins forwarding data between accepted and dialed connections.
func (f *forwarder) Start(accepted net.Conn, dialed net.Conn, handler EventHandler) error {
	select {
	case <-f.g.CloseC():
		return routines.ErrClosed
	case f.counter <- struct{}{}:
	default:
		return ErrConnLimit
	}
	f.addConn(accepted, dialed)
	cleanup := func() {
		f.delConn(accepted, dialed)
		<-f.counter
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
			handler.OnHalfClose(src, err)
		}
		if remaining.Add(-1) == 0 {
			cleanup()
			if handler != nil {
				handler.OnDone()
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
		// The second direction never ran; synthesise its OnHalfClose (src=accepted)
		// so the handler always receives exactly two calls.
		if handler != nil {
			handler.OnHalfClose(accepted, err)
		}
		if remaining.Add(-1) == 0 {
			// First goroutine already finished; we are responsible for cleanup.
			cleanup()
			if handler != nil {
				handler.OnDone()
			}
		}
		return err
	}
	return nil
}

// Count returns the current number of active connections
func (f *forwarder) Count() int {
	return len(f.counter)
}

// Close closes all active connections managed by the forwarder
func (f *forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for conn := range f.conn {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warningf("close: %s", formats.Error(err))
		}
	}
}
