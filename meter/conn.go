package meter

import (
	"net"
	"sync/atomic"
)

type ConnMetrics struct {
	Read    uint64
	Written uint64
}

func Conn(c net.Conn, m *ConnMetrics) net.Conn {
	return &conn{c, m}
}

type conn struct {
	net.Conn
	m *ConnMetrics
}

func (c *conn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	atomic.AddUint64(&c.m.Read, uint64(n))
	return
}

func (c *conn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	atomic.AddUint64(&c.m.Written, uint64(n))
	return
}
