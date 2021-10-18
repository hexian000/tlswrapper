package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

type ServerConfig struct {
	Listen  string `json:"listen"`
	Forward string `json:"forward"`
}

type ClientConfig struct {
	Listen string `json:"listen"`
	Dial   string `json:"dial"`
}

// Config file
type Config struct {
	ServerName         string         `json:"sni"`
	Server             []ServerConfig `json:"server"`
	Client             []ClientConfig `json:"client"`
	Certificate        string         `json:"cert"`
	PrivateKey         string         `json:"key"`
	AuthorizedCerts    []string       `json:"authcerts"`
	NoDelay            bool           `json:"nodelay"`
	ReadBuffer         int            `json:"recvbuf"`
	WriteBuffer        int            `json:"sendbuf"`
	Linger             int            `json:"linger"`
	KeepAlive          int            `json:"keepalive"`
	IdleTimeout        int            `json:"idletimeout"`
	AcceptBacklog      int            `json:"backlog"`
	SessionWindow      uint32         `json:"window"`
	WriteTimeout       int            `json:"writetimeout"`
	StreamCloseTimeout int            `json:"streamclosetimeout"`
}

var defaultConfig = Config{
	ServerName:         "example.com",
	NoDelay:            false,
	ReadBuffer:         0,   // system default
	WriteBuffer:        0,   // system default
	Linger:             -1,  // system default
	KeepAlive:          60,  // every 30s
	IdleTimeout:        900, // 15min
	AcceptBacklog:      16,
	SessionWindow:      256 * 1024, // 256 KiB
	WriteTimeout:       30,
	StreamCloseTimeout: 60,
}

// SetConnParams sets TCP params
func (c *Config) SetConnParams(conn net.Conn) {
	tcpConn := conn.(*net.TCPConn)
	if tcpConn != nil {
		_ = tcpConn.SetNoDelay(c.NoDelay)
		_ = tcpConn.SetLinger(c.Linger)
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
		if c.ReadBuffer > 0 {
			_ = tcpConn.SetReadBuffer(c.ReadBuffer)
		}
		if c.WriteBuffer > 0 {
			_ = tcpConn.SetWriteBuffer(c.WriteBuffer)
		}
	}
}

// NewTLSConfig creates tls.Config
func (c *Config) NewTLSConfig() *tls.Config {
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		log.Fatalln("load local cert:", err)
	}
	certPool := x509.NewCertPool()
	for _, path := range c.AuthorizedCerts {
		certBytes, err := ioutil.ReadFile(path)
		if err != nil {
			log.Fatalln("read authorized certs:", path, err)
		}
		ok := certPool.AppendCertsFromPEM(certBytes)
		if !ok {
			log.Fatalln("append authorized certs:", path)
		}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		RootCAs:      certPool,
		ServerName:   c.ServerName,
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	}
}

// NewMuxConfig creates yamux.Config
func (c *Config) NewMuxConfig() *yamux.Config {
	enableKeepAlive := c.KeepAlive > 0
	// A temporary workaround for passing yamux.VerifyConfig
	if !enableKeepAlive {
		c.KeepAlive = 30
	}
	return &yamux.Config{
		AcceptBacklog:          c.AcceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      time.Duration(c.KeepAlive) * time.Second,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    c.SessionWindow,
		StreamCloseTimeout:     time.Duration(c.StreamCloseTimeout) * time.Second,
		Logger:                 log.Default(),
	}
}
