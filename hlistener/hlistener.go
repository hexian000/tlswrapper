package hlistener

import (
	"math/rand"
	"net"
	"sync/atomic"
)

type Config struct {
	Start, Full  uint32
	Rate         float64
	Unauthorized func() uint32
}

type Listener struct {
	l     net.Listener
	c     Config
	stats struct {
		Accepted atomic.Uint64
		Served   atomic.Uint64
	}
}

func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.l.Accept()
		if err != nil {
			return conn, err
		}
		l.stats.Accepted.Add(1)
		n := l.c.Unauthorized()
		refuse := false
		if n >= l.c.Start {
			if n >= l.c.Full {
				refuse = true
			} else {
				refuse = rand.Float64() < l.c.Rate
			}
		}
		if refuse {
			_ = conn.Close()
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
