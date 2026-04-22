// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package forwarder

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/routines"
	"github.com/hexian000/gosnippets/slog"
)

// ErrConnLimit is returned when the maximum number of concurrent connections is exceeded
var ErrConnLimit = errors.New("connection limit is exceeded")

// Forwarder interface defines methods for forwarding connections
type Forwarder interface {
	Forward(accepted net.Conn, dialed net.Conn) error
	ForwardSync(accepted net.Conn, dialed net.Conn) error
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

func (f *forwarder) connCopy(dst net.Conn, src net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			slog.Stackf(slog.LevelError, 0, "panic: %v", err)
		}
	}()
	_, err := io.Copy(dst, src)
	if err != nil &&
		!errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, syscall.EPIPE) {
		slog.Warningf("stream error: %s", formats.Error(err))
		return
	}
}

// Forward forwards data between accepted and dialed connections
func (f *forwarder) Forward(accepted net.Conn, dialed net.Conn) error {
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
	cleanupOnce := &sync.Once{}
	if err := f.g.Go(func() {
		defer cleanupOnce.Do(cleanup)
		f.connCopy(accepted, dialed)
	}); err != nil {
		cleanupOnce.Do(cleanup)
		return err
	}
	if err := f.g.Go(func() {
		defer cleanupOnce.Do(cleanup)
		f.connCopy(dialed, accepted)
	}); err != nil {
		cleanupOnce.Do(cleanup)
		return err
	}
	return nil
}

// ForwardSync forwards data between accepted and dialed connections and blocks until both directions are done.
func (f *forwarder) ForwardSync(accepted net.Conn, dialed net.Conn) error {
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
	var wg sync.WaitGroup
	wg.Add(2)
	cleanupOnce := &sync.Once{}
	run := func(dst, src net.Conn) {
		defer wg.Done()
		f.connCopy(dst, src)
		// closing both connections unblocks the other direction
		cleanupOnce.Do(func() {
			_ = accepted.Close()
			_ = dialed.Close()
		})
	}
	if err := f.g.Go(func() { run(accepted, dialed) }); err != nil {
		cleanup()
		return err
	}
	if err := f.g.Go(func() { run(dialed, accepted) }); err != nil {
		// first goroutine already started; signal it to stop
		cleanupOnce.Do(func() {
			_ = accepted.Close()
			_ = dialed.Close()
		})
		wg.Wait()
		cleanup()
		return err
	}
	wg.Wait()
	cleanup()
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
