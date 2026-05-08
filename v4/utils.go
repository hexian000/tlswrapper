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

// ioClose logs close failures instead of dropping them.
func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Println(2, slog.LevelWarning, nil, msg)
	}
}

type tcpConn interface {
	SetNoDelay(bool) error
	SetKeepAlive(bool) error
	SetReadBuffer(bytes int) error
	SetWriteBuffer(bytes int) error
}

// setTCPConnParams applies configured socket options when conn exposes them.
func setTCPConnParams(tcp config.TCP, conn net.Conn) {
	tcpConn, ok := conn.(tcpConn)
	if !ok {
		return
	}
	if err := tcpConn.SetNoDelay(tcp.NoDelay); err != nil {
		slog.Warningf("SetNoDelay: %s", formats.Error(err))
	}
	if err := tcpConn.SetKeepAlive(tcp.KeepAlive); err != nil {
		slog.Warningf("SetKeepAlive: %s", formats.Error(err))
	}
	if tcp.ReadBuffer > 0 {
		if err := tcpConn.SetReadBuffer(tcp.ReadBuffer); err != nil {
			slog.Warningf("SetReadBuffer %d: %s", tcp.ReadBuffer, formats.Error(err))
		}
	}
	if tcp.WriteBuffer > 0 {
		if err := tcpConn.SetWriteBuffer(tcp.WriteBuffer); err != nil {
			slog.Warningf("SetWriteBuffer %d: %s", tcp.WriteBuffer, formats.Error(err))
		}
	}
}
