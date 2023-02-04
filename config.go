package tlswrapper

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/slog"
)

type TunnelConfig struct {
	// server listen address
	MuxListen string `json:"muxlisten"`
	// client dial address
	MuxDial string `json:"muxdial"`
	// port forwarding listen address
	Listen string `json:"listen"`
	// reverse port forwarding dial address
	Dial string `json:"dial"`
}

// Config file
type Config struct {
	Tunnels []TunnelConfig `json:"tunnel"`
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
	// (optional) session idle timeout in seconds, default to 900 (15min)
	IdleTimeout int `json:"idletimeout"`
	// (optional) max concurrent connections, default to 4096
	MaxConn int `json:"maxconn"`
	// (optional) mux accept backlog, default to 16, you may not want to change this
	AcceptBacklog int `json:"backlog"`
	// (optional) stream window size in bytes, default to 256KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
	// (optional) generic request timeout in seconds, default to 30, increase on long fat networks
	RequestTimeout int `json:"timeout"`
	// (optional) data write request timeout in seconds, default to 30, used to detect network failes early, increase on slow networks
	WriteTimeout int `json:"writetimeout"`
	// (optional) log output, default to stderr
	Log string `json:"log"`
	// (optional) log output, default to 2 (info)
	LogLevel int `json:"loglevel"`
}

var DefaultConfig = Config{
	ServerName:        "example.com",
	NoDelay:           true,
	KeepAlive:         25, // every 25s
	ServerKeepAlive:   0,
	StartupLimitStart: 10,
	StartupLimitRate:  30,
	StartupLimitFull:  60,
	IdleTimeout:       900, // 15min
	MaxConn:           4096,
	AcceptBacklog:     16,
	StreamWindow:      256 * 1024, // 256 KiB
	RequestTimeout:    30,
	WriteTimeout:      30,
	Log:               "stderr",
	LogLevel:          slog.LevelInfo,
}

// SetConnParams sets TCP params
func (c *Config) SetConnParams(conn net.Conn) {
	if tcpConn := conn.(*net.TCPConn); tcpConn != nil {
		_ = tcpConn.SetNoDelay(c.NoDelay)
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
	}
}

// NewTLSConfig creates tls.Config
func (c *Config) NewTLSConfig(sni string) (*tls.Config, error) {
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
