package main

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/hlistener"
	"github.com/hexian000/tlswrapper/slog"
)

type ServerConfig struct {
	// server-side bind address
	Listen string `json:"listen"`
	// client-side connect addresses
	Dial []string `json:"dial"`
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
	// (optional) client-side keep alive interval in seconds, default to 25 (every 25s)
	KeepAlive int `json:"keepalive"`
	// (optional) server-side keep alive interval in seconds, default to 300 (every 5min)
	ServerKeepAlive int `json:"serverkeepalive"`
	// (optional) soft limit of concurrent unauthenticated connections, default to 10
	StartupLimitStart int `json:"startuplimitstart"`
	// (optional) probability of random disconnection when soft limit is exceeded, default to 30 (30%)
	StartupLimitRate int `json:"startuplimitrate"`
	// (optional) hard limit of concurrent unauthenticated connections, default to 60
	StartupLimitFull int `json:"startuplimitfull"`
	// (optional) session idle timeout in seconds, default to 7200 (2hrs)
	IdleTimeout int `json:"idletimeout"`
	// (optional) mux accept backlog, default to 8, you may not want to change this
	AcceptBacklog int `json:"backlog"`
	// (optional) stream window size in bytes, default to 256KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
	// (optional) authentication timeout in seconds, default to 30
	AuthTimeout int `json:"authtimeout"`
	// (optional) dial timeout in seconds, default to 30
	DialTimeout int `json:"dialtimeout"`
	// (optional) connection timeout in seconds, default to 30
	Timeout int `json:"timeout"`
}

type LocalConfig struct {
	// bind address to serve TCP clients
	Listen string `json:"listen"`
	// upstream TCP service address
	Forward string `json:"forward"`
	// (optional) dial timeout in seconds, default to 30
	DialTimeout int `json:"dialtimeout"`
}

// Config file
type Config struct {
	Server ServerConfig `json:"server"`
	Local  LocalConfig  `json:"local"`
	// (optional) log output, default to stderr
	Log string `json:"log"`
	// (optional) log output, default to 2 (info)
	LogLevel int `json:"loglevel"`
}

var defaultConfig = Config{
	Server: ServerConfig{
		ServerName:        "example.com",
		NoDelay:           true,
		KeepAlive:         25,  // every 25s
		ServerKeepAlive:   300, // every 5min
		StartupLimitStart: 10,
		StartupLimitRate:  30,
		StartupLimitFull:  60,
		IdleTimeout:       7200, // 2hrs
		AcceptBacklog:     8,
		StreamWindow:      256 * 1024, // 256 KiB
		AuthTimeout:       30,
		DialTimeout:       30,
		Timeout:           30,
	},
	Local: LocalConfig{
		DialTimeout: 30,
	},
	Log:      "stderr",
	LogLevel: 2,
}

// LoadTLSConfig loads tls.Config
func (c *Config) LoadTLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(c.Server.Certificate, c.Server.PrivateKey)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	for _, path := range c.Server.AuthorizedCerts {
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
		ServerName:   c.Server.ServerName,
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	}, nil
}

// Timeout gets the generic request timeout
func (c *Config) AuthTimeout() time.Duration {
	return time.Duration(c.Server.AuthTimeout) * time.Second
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
	keepAliveInterval := time.Duration(c.Server.KeepAlive) * time.Second
	if isServer {
		keepAliveInterval = time.Duration(c.Server.ServerKeepAlive) * time.Second
	}
	enableKeepAlive := keepAliveInterval >= time.Second
	if !enableKeepAlive {
		keepAliveInterval = 15 * time.Second
	}
	timeout := time.Duration(c.Server.Timeout) * time.Second
	return &yamux.Config{
		AcceptBacklog:          c.Server.AcceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      keepAliveInterval,
		ConnectionWriteTimeout: timeout,
		MaxStreamWindowSize:    c.Server.StreamWindow,
		StreamOpenTimeout:      timeout,
		StreamCloseTimeout:     timeout,
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}

// NewHardenConfig creates hlistener.Config
func (c *Config) NewHardenConfig(unauthorized func() uint32) *hlistener.Config {
	return &hlistener.Config{
		Start:        uint32(c.Server.StartupLimitStart),
		Full:         uint32(c.Server.StartupLimitFull),
		Rate:         float64(c.Server.StartupLimitRate) / 100.0,
		Unauthorized: unauthorized,
	}
}
