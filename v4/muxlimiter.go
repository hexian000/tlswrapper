// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"math/rand"
	"net"
	"sync/atomic"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// acceptStats reports listener-level accept counters: connections accepted
// from the network and connections admitted for serving.
// Implemented by hlistener.Listener (h2mux) and hardenedMuxListener (h3mux).
type acceptStats interface {
	Stats() (accepted, served uint64)
}

// hardenedMuxListenerConfig mirrors hlistener.Config for mux.Listener.
type hardenedMuxListenerConfig struct {
	Start, Full uint32
	Rate        float64 // 0-1
	MaxSessions uint32
	Stats       func() (numSessions, numHalfOpen uint32)
}

// hardenedMuxListener applies hlistener-equivalent throttling at the
// mux.Session level: sessions accepted while limits are exceeded are closed
// before their handshake runs. h2mux enforces these limits on raw TCP accepts
// via hlistener; h3mux accepts pre-handshake sessions from a QUIC listener,
// so the same policy is enforced here instead.
type hardenedMuxListener struct {
	l     mux.Listener
	c     hardenedMuxListenerConfig
	stats struct {
		total  atomic.Uint64
		served atomic.Uint64
	}
}

func newHardenedMuxListener(l mux.Listener, c hardenedMuxListenerConfig) *hardenedMuxListener {
	return &hardenedMuxListener{l: l, c: c}
}

func (l *hardenedMuxListener) isLimited() bool {
	numSessions, numHalfOpen := l.c.Stats()
	if l.c.MaxSessions > 0 && numSessions > l.c.MaxSessions {
		return true
	}
	if l.c.Full > 0 && numHalfOpen > l.c.Full {
		return true
	}
	if l.c.Start > 0 && numHalfOpen > l.c.Start {
		return rand.Float64() < l.c.Rate
	}
	return false
}

func (l *hardenedMuxListener) Accept() (mux.Session, error) {
	for {
		ss, err := l.l.Accept()
		if err != nil {
			return ss, err
		}
		l.stats.total.Add(1)
		if l.isLimited() {
			_ = ss.Close()
			continue
		}
		l.stats.served.Add(1)
		return ss, nil
	}
}

func (l *hardenedMuxListener) Addr() net.Addr { return l.l.Addr() }

func (l *hardenedMuxListener) Close() error { return l.l.Close() }

func (l *hardenedMuxListener) Stats() (accepted, served uint64) {
	return l.stats.total.Load(), l.stats.served.Load()
}
