package main

import (
	"net"
	"time"
)

// Config file
type Config struct {
	Mode        string `json:"mode"`
	Cipher      string `json:"cipher"`
	Listen      string `json:"listen"`
	Dial        string `json:"dial"`
	Password    string `json:"password"`
	Tag         string `json:"authtag"`
	KeepAlive   int    `json:"keepalive"`
	NoDelay     bool   `json:"nodelay"`
	ReadBuffer  int    `json:"recvbuf"`
	WriteBuffer int    `json:"sendbuf"`
	Linger      int    `json:"linger"`
}

var defaultConfig = Config{
	Mode:        "client",
	Cipher:      "chacha20poly1305",
	Tag:         "tcpcrypt :)",
	KeepAlive:   300,
	NoDelay:     false,
	ReadBuffer:  0, // for system default
	WriteBuffer: 0,
	Linger:      -1,
}

// IsServer checks the config is in server mode
func (c *Config) IsServer() bool {
	return c.Mode == "server"
}

// SetConnParams sets TCP params
func (c *Config) SetConnParams(conn net.Conn) {
	tcpConn := conn.(*net.TCPConn)
	if tcpConn != nil {
		_ = tcpConn.SetNoDelay(c.NoDelay)
		_ = tcpConn.SetLinger(c.Linger)
		if c.KeepAlive > 0 {
			_ = tcpConn.SetKeepAlive(true)
			_ = tcpConn.SetKeepAlivePeriod(time.Duration(c.KeepAlive) * time.Second)
		} else if c.KeepAlive == 0 {
			_ = tcpConn.SetKeepAlive(false)
		}
		if c.ReadBuffer > 0 {
			_ = tcpConn.SetReadBuffer(c.ReadBuffer)
		}
		if c.WriteBuffer > 0 {
			_ = tcpConn.SetWriteBuffer(c.WriteBuffer)
		}
	}
}
