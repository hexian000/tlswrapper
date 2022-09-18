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
	closers map[io.Closer]struct{}
}

func New() *Forwarder {
	return &Forwarder{closers: make(map[io.Closer]struct{})}
}

func (f *Forwarder) copyConn(dst net.Conn, src net.Conn) {
	defer f.wg.Done()
	defer func() {
		if err := recover(); err != nil {
			slog.Errorf("forwarder: %v", err)
		}
		f.close(src)
		f.close(dst)
	}()
	_, _ = io.Copy(dst, src)
}

func (f *Forwarder) addCloser(c io.Closer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closers[c] = struct{}{}
}

func (f *Forwarder) close(c io.Closer) {
	_ = c.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.closers, c)
}

func (f *Forwarder) Forward(accepted net.Conn, dialed net.Conn) {
	f.addCloser(accepted)
	f.addCloser(dialed)
	f.wg.Add(2)
	go f.copyConn(accepted, dialed)
	go f.copyConn(dialed, accepted)
}

func (f *Forwarder) Close() {
	closeList := make([]io.Closer, 0)
	func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for c := range f.closers {
			closeList = append(closeList, c)
		}
	}()
	for _, c := range closeList {
		f.close(c)
	}
	f.wg.Wait()
}
