// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package forwarder

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/routines"
	"github.com/hexian000/gosnippets/slog"
)

var ErrConnLimit = errors.New("connection limit is exceeded")

type Forwarder interface {
	Forward(accepted net.Conn, dialed net.Conn) error
	Count() int
	Close()
}

type forwarder struct {
	mu      sync.Mutex
	g       routines.Group
	conn    map[net.Conn]struct{}
	counter chan struct{}
	closeCh chan struct{}
}

func New(maxConn int, g routines.Group) Forwarder {
	return &forwarder{
		conn:    make(map[net.Conn]struct{}),
		counter: make(chan struct{}, maxConn),
		closeCh: make(chan struct{}),
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
		!errors.Is(err, syscall.EPIPE) &&
		!errors.Is(err, yamux.ErrStreamClosed) {
		slog.Warningf("stream error: %s", formats.Error(err))
		return
	}
}

func (f *forwarder) Forward(accepted net.Conn, dialed net.Conn) error {
	select {
	case <-f.closeCh:
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

func (f *forwarder) Count() int {
	return len(f.counter)
}

func (f *forwarder) Close() {
	close(f.closeCh)
	f.mu.Lock()
	defer f.mu.Unlock()
	for conn := range f.conn {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warningf("close: %s", formats.Error(err))
		}
	}
}
