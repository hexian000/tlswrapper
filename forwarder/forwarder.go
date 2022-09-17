package forwarder

import (
	"io"
	"net"
	"sync"

	"github.com/hexian000/tlswrapper/slog"
)

type Forwarder struct {
	wg      sync.WaitGroup
	mu      sync.Mutex
	closers map[io.Closer]io.Closer
}

func New() *Forwarder {
	return &Forwarder{closers: make(map[io.Closer]io.Closer)}
}

func (f *Forwarder) copyConn(dst net.Conn, src net.Conn) {
	defer f.wg.Done()
	defer func() {
		_ = src.Close()
		_ = dst.Close()
	}()
	defer func() {
		if err := recover(); err != nil {
			slog.Errorf("forwarder: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.closers, src)
	}()
	func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closers[src] = dst
	}()
	_, _ = io.Copy(dst, src)
}

func (f *Forwarder) Forward(accepted net.Conn, dialed net.Conn) {
	f.wg.Add(2)
	go f.copyConn(accepted, dialed)
	go f.copyConn(dialed, accepted)
}

func (f *Forwarder) NumForwards() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closers)
}

func (f *Forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for src, dst := range f.closers {
		_ = src.Close()
		_ = dst.Close()
		delete(f.closers, src)
	}
	f.wg.Wait()
}
