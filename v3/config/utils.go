package config

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/slog"
)

// Timeout gets the generic request timeout
func (c *File) Timeout() time.Duration {
	return time.Duration(c.ConnectTimeout) * time.Second
}

// FindService finds dial address by service name
func (c *File) FindService(service string) string {
	if service == "" {
		return ""
	}
	return c.Services[service]
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
func (c *File) NewMuxConfig() *yamux.Config {
	keepAliveInterval := time.Duration(c.ServerKeepAlive) * time.Second
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
