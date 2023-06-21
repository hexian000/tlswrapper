package hlistener

import (
	"math/rand"
	"net"
	"sync/atomic"
)

type Stats struct {
	Accepted atomic.Uint64
	Refused  atomic.Uint64
}

type Config struct {
	Start, Full  uint32
	Rate         float64
	Unauthorized func() uint32
}

type Listener struct {
	l net.Listener
	c Config
	// atomic vars need to be aligned
	s *Stats
}

func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.l.Accept()
		if err != nil {
			return conn, err
		}
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
			if l.s != nil {
				l.s.Refused.Add(1)
			}
			continue
		}
		if l.s != nil {
			l.s.Accepted.Add(1)
		}
		return conn, err
	}
}

func (l *Listener) Close() error {
	return l.l.Close()
}

func (l *Listener) Addr() net.Addr {
	return l.l.Addr()
}

// Wrap the raw listener
func Wrap(l net.Listener, c *Config, s *Stats) *Listener {
	return &Listener{l: l, c: *c, s: s}
}
