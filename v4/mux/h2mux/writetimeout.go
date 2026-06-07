// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"net"
	"time"
)

// writeTimeoutConn sets a per-Write deadline and clears it afterwards,
// providing connection-level write timeout detection without OS-specific
// socket options.
type writeTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *writeTimeoutConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(b)
	// Always clear the deadline so reads and future writes are not affected.
	_ = c.Conn.SetWriteDeadline(time.Time{})
	return n, err
}
