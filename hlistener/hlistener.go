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

type Stats struct {
	Accepted, Refused uint64
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
			atomic.AddUint64(&l.s.Refused, 1)
			continue
		}
		atomic.AddUint64(&l.s.Accepted, 1)
		return conn, err
	}
}

func (l *Listener) Close() error {
	return l.l.Close()
}

func (l *Listener) Addr() net.Addr {
	return l.l.Addr()
}

func (l *Listener) Stat() Stats {
	return Stats{
		Accepted: atomic.LoadUint64(&l.s.Accepted),
		Refused:  atomic.LoadUint64(&l.s.Refused),
	}
}

// Wrap the raw listener
func Wrap(l net.Listener, c *Config) net.Listener {
	return &Listener{l: l, c: *c, s: &Stats{}}
}
