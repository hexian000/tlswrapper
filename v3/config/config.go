package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/slog"
)

// Tunnel represents a fixed tunnel between 2 peers
type Tunnel struct {
	// (optional) is disabled
	Disabled bool `json:"disabled,omitempty"`
	// (optional) mux dial address
	MuxDial string `json:"addr,omitempty"`
	// (optional) local listener address
	Listen string `json:"listen,omitempty"`
	// remote service name
	PeerService string `json:"peerservice"`
	// (optional) keep tunnels connected
	Redial bool `json:"redial"`
	// (optional) client-side keep alive interval in seconds, default to 25 (every 25s)
	KeepAlive int `json:"keepalive"`
	// (optional) mux accept backlog, default to 256, you may not want to change this
	AcceptBacklog int `json:"backlog"`
	// (optional) stream window size in bytes, default to 256 KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
}

// File config file
type File struct {
	// (optional) local peer name
	PeerName string `json:"peername,omitempty"`
	// (optional) mux listen address
	MuxListen string `json:"muxlisten,omitempty"`
	// service name to dial address
	Services map[string]string `json:"services"`
	// peer name to config
	Peers map[string]Tunnel `json:"peers"`
	// (optional) health check and metrics, default to "" (disabled)
	HTTPListen string `json:"httplisten,omitempty"`
	// TLS: (optional) SNI field in handshake, default to "example.com"
	ServerName string `json:"sni"`
	// TLS: local certificate
	Certificate string `json:"cert"`
	// TLS: local private key
	PrivateKey string `json:"key"`
	// TLS: authorized remote certificates, bundle supported
	AuthorizedCerts []string `json:"authcerts"`
	// (optional) TCP no delay, default to true
	NoDelay bool `json:"nodelay"`
	// (optional) soft limit of concurrent unauthenticated connections, default to 10
	StartupLimitStart int `json:"startuplimitstart"`
	// (optional) probability of random disconnection when soft limit is exceeded, default to 30 (30%)
	StartupLimitRate int `json:"startuplimitrate"`
	// (optional) hard limit of concurrent unauthenticated connections, default to 60
	StartupLimitFull int `json:"startuplimitfull"`
	// (optional) max concurrent streams, default to 16384
	MaxConn int `json:"maxconn"`
	// (optional) max concurrent incoming sessions, default to 128
	MaxSessions int `json:"maxsessions"`
	// (optional) server-side keep alive interval in seconds, default to 300 (every 5min)
	ServerKeepAlive int `json:"serverkeepalive"`
	// (optional) tunnel connecting timeout in seconds, default to 15
	ConnectTimeout int `json:"timeout"`
	// (optional) stream open timeout in seconds, default to 30
	StreamOpenTimeout int `json:"streamopentimeout"`
	// (optional) stream close timeout in seconds, default to 120
	StreamCloseTimeout int `json:"streamclosetimeout"`
	// (optional) data write request timeout in seconds, default to 15, used to detect network failes early
	WriteTimeout int `json:"writetimeout"`
	// (optional) log output, default to stdout
	Log string `json:"log,omitempty"`
	// (optional) log output, default to 4 (notice)
	LogLevel int `json:"loglevel"`
}

var DefaultConfig = File{
	ServerName:         "example.com",
	NoDelay:            true,
	StartupLimitStart:  10,
	StartupLimitRate:   30,
	StartupLimitFull:   60,
	MaxConn:            16384,
	MaxSessions:        128,
	ServerKeepAlive:    300, // every 5min
	ConnectTimeout:     15,
	StreamOpenTimeout:  30,
	StreamCloseTimeout: 120,
	WriteTimeout:       15,
	Log:                "stdout",
	LogLevel:           slog.LevelNotice,
}

var DefaultTunnelConfig = Tunnel{
	Redial:        true,
	KeepAlive:     25, // every 25s
	AcceptBacklog: 256,
	StreamWindow:  256 * 1024, // 256 KiB
}

func Load(b []byte) (*File, error) {
	cfg := DefaultConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	slog.Default().SetLevel(cfg.LogLevel)
	if err := slog.Default().SetOutputConfig(cfg.Log, "tlswrapper"); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ReadFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(b)
}

func rangeCheckInt(key string, value int, min int, max int) error {
	if !(min <= value && value <= max) {
		return fmt.Errorf("%s is out of range (%d - %d)", key, min, max)
	}
	return nil
}

func (c *File) Validate() error {
	// TODO
	// if err := rangeCheckInt("keepalive", c.KeepAlive, 0, 86400); err != nil {
	// 	return err
	// }
	// if err := rangeCheckInt("serverkeepalive", c.ServerKeepAlive, 0, 86400); err != nil {
	// 	return err
	// }
	if err := rangeCheckInt("startuplimitstart", c.StartupLimitStart, 1, math.MaxInt); err != nil {
		return err
	}
	if err := rangeCheckInt("startuplimitrate", c.StartupLimitRate, 0, 100); err != nil {
		return err
	}
	if err := rangeCheckInt("startuplimitfull", c.StartupLimitFull, 1, math.MaxInt); err != nil {
		return err
	}
	if err := rangeCheckInt("maxconn", c.MaxConn, 1, math.MaxInt); err != nil {
		return err
	}
	if err := rangeCheckInt("maxsessions", c.MaxSessions, 1, math.MaxInt); err != nil {
		return err
	}
	return nil
}

// SetConnParams sets TCP params
func (c *File) SetConnParams(conn net.Conn) {
	if tcpConn := conn.(*net.TCPConn); tcpConn != nil {
		_ = tcpConn.SetNoDelay(c.NoDelay)
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
	}
}

// NewTLSConfig creates tls.Config
func (c *File) NewTLSConfig(sni string) (*tls.Config, error) {
	if sni == "" {
		sni = c.ServerName
	}
	if c.Certificate == "" && c.PrivateKey == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	for _, path := range c.AuthorizedCerts {
		certBytes, err := os.ReadFile(path)
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
func (c *File) Timeout() time.Duration {
	return time.Duration(c.ConnectTimeout) * time.Second
}

type logWrapper struct {
	*slog.Logger
}

func (w *logWrapper) Write(p []byte) (n int, err error) {
	const calldepth = 4
	raw := strings.TrimSuffix(string(p), "\n")
	if msg := strings.TrimPrefix(raw, "[ERR] "); len(msg) != len(raw) {
		w.Output(calldepth, slog.LevelError, []byte(msg))
	} else if msg := strings.TrimPrefix(raw, "[WARN] "); len(msg) != len(raw) {
		w.Output(calldepth, slog.LevelWarning, []byte(msg))
	} else {
		w.Output(calldepth, slog.LevelError, p)
	}
	return len(p), nil
}

// NewMuxConfig creates yamux.Config
func (c *File) FindService(service string) string {
	if service == "" {
		return ""
	}
	return c.Services[service]
}

// NewMuxConfig creates yamux.Config
func (c *File) NewMuxConfig() *yamux.Config {
	t := DefaultTunnelConfig
	keepAliveInterval := time.Duration(c.ServerKeepAlive) * time.Second
	enableKeepAlive := keepAliveInterval >= time.Second
	if !enableKeepAlive {
		keepAliveInterval = 15 * time.Second
	}
	return &yamux.Config{
		AcceptBacklog:          t.AcceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      keepAliveInterval,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    t.StreamWindow,
		StreamOpenTimeout:      time.Duration(c.StreamOpenTimeout) * time.Second,
		StreamCloseTimeout:     time.Duration(c.StreamCloseTimeout) * time.Second,
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}

// NewMuxConfig creates yamux.Config
func (t *Tunnel) NewMuxConfig(c *File) *yamux.Config {
	keepAliveInterval := time.Duration(t.KeepAlive) * time.Second
	enableKeepAlive := keepAliveInterval >= time.Second
	if !enableKeepAlive {
		keepAliveInterval = 15 * time.Second
	}
	return &yamux.Config{
		AcceptBacklog:          t.AcceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      keepAliveInterval,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    t.StreamWindow,
		StreamOpenTimeout:      time.Duration(c.StreamOpenTimeout) * time.Second,
		StreamCloseTimeout:     time.Duration(c.StreamCloseTimeout) * time.Second,
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}
