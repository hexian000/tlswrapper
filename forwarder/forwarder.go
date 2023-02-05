package forwarder

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/routines"
	"github.com/hexian000/tlswrapper/slog"
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
		counter: make(chan struct{}, maxConn*2),
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
	_ = accepted.Close()
	_ = dialed.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conn, accepted)
	delete(f.conn, dialed)
}

func (f *forwarder) connCopy(dst net.Conn, src net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("panic:", err)
		}
	}()
	_, err := io.Copy(dst, src)
	if err != nil &&
		!errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, syscall.EPIPE) &&
		!errors.Is(err, yamux.ErrStreamClosed) {
		slog.Warningf("stream error: [%T] %v", err, err)
		return
	}
}

func (f *forwarder) Forward(accepted net.Conn, dialed net.Conn) error {
	select {
	case <-f.closeCh:
		return routines.ErrStopping
	case f.counter <- struct{}{}:
	default:
		return ErrConnLimit
	}
	f.addConn(accepted, dialed)
	cleanupOnce := &sync.Once{}
	cleanup := func() {
		cleanupOnce.Do(func() {
			f.delConn(accepted, dialed)
			<-f.counter
		})
	}
	if err := f.g.Go(func() {
		defer cleanup()
		f.connCopy(accepted, dialed)
	}); err != nil {
		cleanup()
		return err
	}
	if err := f.g.Go(func() {
		defer cleanup()
		f.connCopy(dialed, accepted)
	}); err != nil {
		cleanup()
		return err
	}
	return nil
}

func (f *forwarder) Count()int {
	return len(f.counter)
}

func (f *forwarder) Close() {
	close(f.closeCh)
	f.mu.Lock()
	defer f.mu.Unlock()
	for conn := range f.conn {
		_ = conn.Close()
	}
}
