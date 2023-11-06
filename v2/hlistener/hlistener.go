package hlistener

import (
	"math/rand"
	"net"
	"sync/atomic"

	"github.com/hexian000/tlswrapper/v2/slog"
)

type ServerStats struct {
	Sessions uint32
	HalfOpen uint32
}

type Config struct {
	Start, Full uint32
	Rate        float64
	MaxSessions uint32
	Stats       func() ServerStats
}

type Listener struct {
	l     net.Listener
	c     Config
	stats struct {
		Accepted atomic.Uint64
		Served   atomic.Uint64
	}
}

func (l *Listener) isLimited() bool {
	stats := l.c.Stats()
	if l.c.MaxSessions > 0 && stats.Sessions >= l.c.MaxSessions {
		return true
	}
	if stats.HalfOpen >= l.c.Full {
		return true
	}
	if stats.HalfOpen >= l.c.Start {
		return rand.Float64() < l.c.Rate
	}
	return false
}

func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.l.Accept()
		if err != nil {
			return conn, err
		}
		l.stats.Accepted.Add(1)
		if l.isLimited() {
			if err := conn.Close(); err != nil {
				slog.Warningf("close: (%T) %v", err, err)
			}
			continue
		}
		l.stats.Served.Add(1)
		return conn, err
	}
}

func (l *Listener) Close() error {
	return l.l.Close()
}

func (l *Listener) Addr() net.Addr {
	return l.l.Addr()
}

func (l *Listener) Stats() (accepted uint64, served uint64) {
	return l.stats.Accepted.Load(), l.stats.Served.Load()
}

// Wrap the raw listener
func Wrap(l net.Listener, c *Config) *Listener {
	return &Listener{l: l, c: *c}
}
