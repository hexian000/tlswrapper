package main

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"time"

	"github.com/hexian000/tlswrapper/hlistener"
	"github.com/hexian000/tlswrapper/slog"
	"github.com/xtaci/smux"
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
	// (optional) server-side keep alive interval in seconds, default to 0 (disabled)
	ServerKeepAlive int `json:"serverkeepalive"`
	// (optional) soft limit of concurrent unauthenticated connections, default to 10
	StartupLimitStart int `json:"startuplimitstart"`
	// (optional) probability of random disconnection when soft limit is exceeded, default to 30 (30%)
	StartupLimitRate int `json:"startuplimitrate"`
	// (optional) hard limit of concurrent unauthenticated connections, default to 60
	StartupLimitFull int `json:"startuplimitfull"`
	// (optional) stream window size in bytes, default to 256KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
	// (optional) authentication timeout in seconds, default to 30
	AuthTimeout int `json:"authtimeout"`
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
		KeepAlive:         10, // every 10s
		ServerKeepAlive:   0,  // disabled
		StartupLimitStart: 10,
		StartupLimitRate:  30,
		StartupLimitFull:  60,
		StreamWindow:      256 * 1024, // 256 KiB
		AuthTimeout:       30,
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

// NewMuxConfig creates yamux.Config
func (c *Config) NewMuxConfig(isServer bool) *smux.Config {
	keepAliveInterval := time.Duration(c.Server.KeepAlive) * time.Second
	if isServer {
		keepAliveInterval = time.Duration(c.Server.ServerKeepAlive) * time.Second
	}
	timeout := time.Duration(c.Server.Timeout) * time.Second
	cfg := &smux.Config{
		Version:           2,
		KeepAliveDisabled: keepAliveInterval < time.Second,
		KeepAliveInterval: keepAliveInterval,
		KeepAliveTimeout:  timeout,
		MaxFrameSize:      16384,
		MaxReceiveBuffer:  8 * int(c.Server.StreamWindow),
		MaxStreamBuffer:   int(c.Server.StreamWindow),
	}
	if err := smux.VerifyConfig(cfg); err != nil {
		slog.Errorf("mux config error: %v", err)
		return nil
	}
	return cfg
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
