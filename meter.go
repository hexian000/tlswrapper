package main

import (
	"net"
	"sync/atomic"
)

func Meter(conn net.Conn) *MeteredConn {
	return &MeteredConn{Conn: conn}
}

type MeteredConn struct {
	net.Conn
	r, w uint64
}

func (c *MeteredConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	atomic.AddUint64(&c.r, uint64(n))
	return
}

func (c *MeteredConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	atomic.AddUint64(&c.w, uint64(n))
	return
}

func (c *MeteredConn) Count() (read uint64, write uint64) {
	read = atomic.LoadUint64(&c.r)
	write = atomic.LoadUint64(&c.w)
	return
}
