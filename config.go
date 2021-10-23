package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"time"
	"tlswrapper/slog"

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
	Linger             int            `json:"linger"`
	KeepAlive          int            `json:"keepalive"`
	IdleTimeout        int            `json:"idletimeout"`
	AcceptBacklog      int            `json:"backlog"`
	SessionWindow      uint32         `json:"window"`
	ConnectTimeout     int            `json:"connecttimeout"`
	WriteTimeout       int            `json:"writetimeout"`
	StreamCloseTimeout int            `json:"streamclosetimeout"`
	UDPLog             string         `json:"udplog"`
}

var defaultConfig = Config{
	ServerName:         "example.com",
	NoDelay:            false,
	Linger:             -1,  // system default
	KeepAlive:          25,  // every 25s
	IdleTimeout:        900, // 15min
	AcceptBacklog:      16,
	SessionWindow:      256 * 1024, // 256 KiB
	ConnectTimeout:     15,
	WriteTimeout:       30,
	StreamCloseTimeout: 60,
}

// SetConnParams sets TCP params
func (c *Config) SetConnParams(conn net.Conn) {
	if tcpConn := conn.(*net.TCPConn); tcpConn != nil {
		_ = tcpConn.SetNoDelay(c.NoDelay)
		_ = tcpConn.SetLinger(c.Linger)
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
	}
}

// NewTLSConfig creates tls.Config
func (c *Config) NewTLSConfig() *tls.Config {
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		log.Println("load local cert:", err)
		return nil
	}
	certPool := x509.NewCertPool()
	for _, path := range c.AuthorizedCerts {
		certBytes, err := ioutil.ReadFile(path)
		if err != nil {
			log.Println("read authorized certs:", path, err)
			return nil
		}
		ok := certPool.AppendCertsFromPEM(certBytes)
		if !ok {
			log.Println("append authorized certs:", path)
			return nil
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

type LogWrapper struct {
	*slog.Logger
}

func (w *LogWrapper) Write(p []byte) (n int, err error) {
	msg := string(p)
	switch true {
	case strings.HasPrefix(msg, "[ERR] "):
		w.Output(3, slog.LevelError, strings.TrimPrefix(msg, "[ERR] "))
	case strings.HasPrefix(msg, "[WARN] "):
		w.Output(3, slog.LevelWarning, strings.TrimPrefix(msg, "[WARN] "))
	default:
		w.Output(3, slog.LevelError, msg)
	}
	return len(p), nil
}

// NewMuxConfig creates yamux.Config
func (c *Config) NewMuxConfig() *yamux.Config {
	return &yamux.Config{
		AcceptBacklog:          c.AcceptBacklog,
		EnableKeepAlive:        false,
		KeepAliveInterval:      30,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    c.SessionWindow,
		StreamCloseTimeout:     time.Duration(c.StreamCloseTimeout) * time.Second,
		Logger:                 log.New(&LogWrapper{slog.Default()}, "", 0),
	}
}
