package meter

import (
	"net"
	"sync/atomic"
)

type ConnMetrics struct {
	Read    atomic.Uint64
	Written atomic.Uint64
}

func Conn(c net.Conn, m *ConnMetrics) net.Conn {
	// early validation
	m.Read.Add(0)
	m.Written.Add(0)
	return &conn{c, m}
}

type conn struct {
	net.Conn
	m *ConnMetrics
}

func (c *conn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	c.m.Read.Add(uint64(n))
	return
}

func (c *conn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	c.m.Written.Add(uint64(n))
	return
}
