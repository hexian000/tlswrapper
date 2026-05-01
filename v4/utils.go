// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"fmt"
	"io"
	"net"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
)

// ioClose closes the given io.Closer and logs any error
func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Println(2, slog.LevelWarning, nil, msg)
	}
}

// setMuxConnParams applies TCP socket options for the mux-layer connection
func setMuxConnParams(mux config.Mux, conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(mux.NoDelay)
	_ = tcpConn.SetKeepAlive(mux.KeepAlive)
}

// setTCPConnParams applies TCP socket options for a local (application-side) connection
func setTCPConnParams(tcp config.TCP, conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(tcp.NoDelay)
	_ = tcpConn.SetKeepAlive(tcp.KeepAlive)
}
