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

const serverName = "example.com"

// Config file
type Config struct {
	Mode            string   `json:"mode"`
	Listen          string   `json:"listen"`
	Dial            string   `json:"dial"`
	Certificate     string   `json:"cert"`
	PrivateKey      string   `json:"key"`
	AuthorizedCerts []string `json:"authcerts"`
	KeepAlive       int      `json:"keepalive"`
	NoDelay         bool     `json:"nodelay"`
	ReadBuffer      int      `json:"recvbuf"`
	WriteBuffer     int      `json:"sendbuf"`
	Linger          int      `json:"linger"`
}

var defaultConfig = Config{
	Mode:        "client",
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
	if c.IsServer() {
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    certPool,
			ServerName:   serverName,
		}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
		ServerName:   serverName,
	}
}

// NewMuxConfig creates yamux.Config
func (c *Config) NewMuxConfig() *yamux.Config {
	return &yamux.Config{
		AcceptBacklog:          16,
		EnableKeepAlive:        false,
		KeepAliveInterval:      30 * time.Second,
		ConnectionWriteTimeout: 10 * time.Second,
		MaxStreamWindowSize:    256 * 1024,
		Logger:                 log.Default(),
	}
}
