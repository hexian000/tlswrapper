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

// Config file
type Config struct {
	ServerName      string   `json:"sni"`
	Listen          string   `json:"listen"`
	Dial            string   `json:"dial"`
	TLSListen       string   `json:"tlslisten"`
	TLSDial         string   `json:"tlsdial"`
	Certificate     string   `json:"cert"`
	PrivateKey      string   `json:"key"`
	AuthorizedCerts []string `json:"authcerts"`
	NoDelay         bool     `json:"nodelay"`
	ReadBuffer      int      `json:"recvbuf"`
	WriteBuffer     int      `json:"sendbuf"`
	Linger          int      `json:"linger"`
	KeepAlive       int      `json:"keepalive"`
	AcceptBacklog   int      `json:"backlog"`
	SessionWindow   uint32   `json:"window"`
	WriteTimeout    int      `json:"writetimeout"`
}

var defaultConfig = Config{
	ServerName:    "example.com",
	NoDelay:       false,
	ReadBuffer:    0, // for system default
	WriteBuffer:   0,
	Linger:        -1,
	KeepAlive:     60,
	AcceptBacklog: 16,
	SessionWindow: 2 * 1024 * 1024, // 2 MiB
	WriteTimeout:  10,
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
	return &yamux.Config{
		AcceptBacklog:          c.AcceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      time.Duration(c.KeepAlive) * time.Second,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    uint32(c.SessionWindow),
		Logger:                 log.Default(),
	}
}
