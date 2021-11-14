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
	// TLS server bind address
	Listen string `json:"listen"`
	// (optional) upstream TCP service address, leave empty or unconfigured to use builtin HTTP proxy
	Forward string `json:"forward"`
}

// ForwardConfig contains configs for a HTTP proxy forward over
// the client mux streams
type ForwardConfig struct {
	// port forward listen address
	Listen string `json:"listen"`
	// port forward to
	Forward string `json:"forward"`
}

// ClientConfig contains configs for a TLS client
type ClientConfig struct {
	// (optional) server hostname, used in local proxy
	HostName string `json:"hostname"`
	// (optional) SNI field in TLS handshake
	ServerName string `json:"sni"`
	// (optional) bind address
	Listen string `json:"listen"`
	// server address
	Dial string `json:"dial"`
	// (optional) HTTP proxy forwarder configs
	ProxyForwards []ForwardConfig `json:"proxy"`
}

// ProxyConfig contains configs for local proxy server
type ProxyConfig struct {
	// (optional) HTTP proxy forwarder configs
	LocalHost string `json:"localhost"`
	// HTTP proxy forwarder configs
	Listen string `json:"listen"`
	// (optional) route rules by host names, maps host name to client[*].hostname, empty for direct
	HostRoutes map[string]string `json:"hostroutes"`
	// (optional) default forward destination, empty for direct
	DefaultRoute string `json:"default"`
	// (optional) disable tlswrapper REST API
	DisableAPI bool `json:"noapi"`
}

// Config file
type Config struct {
	// (optional) SNI field in TLS handshake, default to "example.com"
	ServerName string `json:"sni"`
	// (optional) TLS servers we run
	Server []ServerConfig `json:"server"`
	// (optional) TLS servers we may connect to
	Client []ClientConfig `json:"client"`
	// (optional) Local HTTP proxy server, for automatically route hostnames to proper TLS server
	Proxy ProxyConfig `json:"proxy"`
	// Local TLS certificate
	Certificate string `json:"cert"`
	// Local TLS pricate key
	PrivateKey string `json:"key"`
	// Local TLS authorized certificates, bundle supported
	AuthorizedCerts []string `json:"authcerts"`
	// (optional) TCP no delay, default to false
	NoDelay bool `json:"nodelay"`
	// (optional) TCP linger, default to 30
	Linger int `json:"linger"`
	// (optional) client-side keep alive interval in seconds, default to false since we have an encrypted one
	KeepAlive int `json:"keepalive"`
	// (optional) server-side keep alive interval in seconds, default to 0 (disabled)
	ServerKeepAlive int `json:"serverkeepalive"`
	// (optional) session idle timeout in seconds, default to 900 (15min)
	IdleTimeout int `json:"idletimeout"`
	// (optional) mux accept backlog, default to 8, you may not want to change this
	AcceptBacklog int `json:"backlog"`
	// (optional) stream window size in bytes, default to 256KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
	// (optional) generic request timeout in seconds, default to 262144 (256KiB), increase on long fat networks
	RequestTimeout int `json:"timeout"`
	// (optional) data write request timeout in seconds, default to 30, used to detect network failes early, increase on slow networks
	WriteTimeout int `json:"writetimeout"`
	// (optional) UDP log sink address, if set, log will be send to this address rather than stdout
	UDPLog string `json:"udplog"`
}

var defaultConfig = Config{
	ServerName:     "example.com",
	NoDelay:        false,
	Linger:         30,
	KeepAlive:      15,  // every 15s
	IdleTimeout:    900, // 15min
	AcceptBacklog:  8,
	StreamWindow:   256 * 1024, // 256 KiB
	RequestTimeout: 30,
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
		MaxStreamWindowSize:    c.StreamWindow,
		StreamOpenTimeout:      c.Timeout(),
		StreamCloseTimeout:     c.Timeout(),
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}

const (
	apiDomain = "tlswrapper.api"
)

func getAPIHost(hostname string) (host string, ok bool) {
	const apiSuffix = "." + apiDomain
	ok = len(hostname) > len(apiSuffix) &&
		strings.EqualFold(
			hostname[len(hostname)-len(apiSuffix):],
			apiSuffix,
		)
	if ok {
		host = hostname[:len(hostname)-len(apiSuffix)]
	}
	return
}

func (c *ProxyConfig) findRoute(hostname string) string {
	if apiHost, ok := getAPIHost(hostname); ok {
		if strings.EqualFold(apiHost, c.LocalHost) {
			return ""
		}
		return apiHost
	}
	if route, ok := c.HostRoutes[hostname]; ok {
		return route
	}
	return c.DefaultRoute
}
