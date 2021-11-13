package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/slog"
)

// ServerConfig contains configs for a TLS server
type ServerConfig struct {
	Listen  string `json:"listen"`
	Forward string `json:"forward"`
}

// ForwardConfig contains configs for a HTTP proxy forward over
// the client mux streams
type ForwardConfig struct {
	Listen  string `json:"listen"`
	Forward string `json:"forward"`
}

// ClientConfig contains configs for a TLS client
type ClientConfig struct {
	HostName      string          `json:"hostname"`
	ServerName    string          `json:"sni"`
	Listen        string          `json:"listen"`
	Dial          string          `json:"dial"`
	ProxyForwards []ForwardConfig `json:"proxy"`
}

// ProxyConfig contains configs for local proxy server
type ProxyConfig struct {
	LocalHost    string            `json:"localhost"`
	Listen       string            `json:"listen"`
	HostRoutes   map[string]string `json:"hostroutes"`
	DefaultRoute string            `json:"default"`
	DisableAPI   bool              `json:"noapi"`
}

// Config file
type Config struct {
	ServerName      string         `json:"sni"`
	Server          []ServerConfig `json:"server"`
	Client          []ClientConfig `json:"client"`
	Proxy           ProxyConfig    `json:"proxy"`
	Certificate     string         `json:"cert"`
	PrivateKey      string         `json:"key"`
	AuthorizedCerts []string       `json:"authcerts"`
	NoDelay         bool           `json:"nodelay"`
	Linger          int            `json:"linger"`
	KeepAlive       int            `json:"keepalive"`
	ServerKeepAlive int            `json:"serverkeepalive"`
	IdleTimeout     int            `json:"idletimeout"`
	AcceptBacklog   int            `json:"backlog"`
	SessionWindow   uint32         `json:"window"`
	RequestTimeout  int            `json:"timeout"`
	WriteTimeout    int            `json:"writetimeout"`
	UDPLog          string         `json:"udplog"`
}

var defaultConfig = Config{
	ServerName:     "example.com",
	NoDelay:        false,
	Linger:         30,
	KeepAlive:      15,  // every 15s
	IdleTimeout:    900, // 15min
	AcceptBacklog:  8,
	SessionWindow:  256 * 1024, // 256 KiB
	RequestTimeout: 15,
	WriteTimeout:   30,
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
func (c *Config) NewTLSConfig(sni string) (*tls.Config, error) {
	if sni == "" {
		sni = c.ServerName
	}
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	for _, path := range c.AuthorizedCerts {
		certBytes, err := ioutil.ReadFile(path)
		if err != nil {
			return nil, err
		}
		ok := certPool.AppendCertsFromPEM(certBytes)
		if !ok {
			return nil, err
		}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		RootCAs:      certPool,
		ServerName:   sni,
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	}, nil
}

// Timeout gets the generic request timeout
func (c *Config) Timeout() time.Duration {
	return time.Duration(c.RequestTimeout) * time.Second
}

type logWrapper struct {
	*slog.Logger
}

func (w *logWrapper) Write(p []byte) (n int, err error) {
	const calldepth = 4
	raw := string(p)
	if msg := strings.TrimPrefix(raw, "[ERR] "); len(msg) != len(raw) {
		w.Output(calldepth, slog.LevelError, msg)
	} else if msg := strings.TrimPrefix(raw, "[WARN] "); len(msg) != len(raw) {
		w.Output(calldepth, slog.LevelWarning, msg)
	} else {
		w.Output(calldepth, slog.LevelError, raw)
	}
	return len(p), nil
}

// NewMuxConfig creates yamux.Config
func (c *Config) NewMuxConfig(isServer bool) *yamux.Config {
	keepAliveInterval := time.Duration(c.KeepAlive) * time.Second
	if isServer {
		keepAliveInterval = time.Duration(c.ServerKeepAlive) * time.Second
	}
	enableKeepAlive := keepAliveInterval >= time.Second
	if !enableKeepAlive {
		keepAliveInterval = 15 * time.Second
	}
	return &yamux.Config{
		AcceptBacklog:          c.AcceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      keepAliveInterval,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    c.SessionWindow,
		StreamOpenTimeout:      c.Timeout(),
		StreamCloseTimeout:     c.Timeout(),
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}

func (c *ProxyConfig) findRoute(host string) string {
	if strings.EqualFold(host, c.LocalHost) {
		return ""
	}
	if strings.EqualFold(host, c.LocalHost+apiDomain) {
		return ""
	}
	if route, ok := c.HostRoutes[host]; ok {
		return route
	}
	return c.DefaultRoute
}
