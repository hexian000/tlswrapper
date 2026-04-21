// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
)

// SetLogger sets up logging according to config
func (cfg *File) SetLogger(l *slog.Logger) error {
	switch cfg.Log {
	case "", "stdout":
		l.SetOutput(slog.OutputWriter, os.Stdout)
	case "discard":
		l.SetOutput(slog.OutputDiscard)
	case "stderr":
		l.SetOutput(slog.OutputWriter, os.Stderr)
	case "syslog":
		l.SetOutput(slog.OutputSyslog, "tlswrapper")
	default:
		return fmt.Errorf("unknown log output: %s", cfg.Log)
	}
	l.SetLevel(cfg.LogLevel)
	return nil
}

// newX509CertPool parses a slice of PEM-encoded certificates into an x509.CertPool
func newX509CertPool(authCerts []string) (*x509.CertPool, error) {
	certPool := x509.NewCertPool()
	for i, cert := range authCerts {
		if !certPool.AppendCertsFromPEM([]byte(cert)) {
			return nil, fmt.Errorf("unable to parse authorized certificate #%d", i)
		}
	}
	return certPool, nil
}

// Timeout returns the session inactivity timeout
func (c *File) Timeout() time.Duration {
	return time.Duration(c.SessionTimeout) * time.Second
}

// SetMuxConnParams sets TCP parameters on the mux-layer connection
func (c *File) SetMuxConnParams(conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(c.Mux.NoDelay)
	_ = tcpConn.SetKeepAlive(c.Mux.KeepAlive)
}

// SetTCPConnParams sets TCP parameters on a local (application-side) connection
func (c *File) SetTCPConnParams(conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(c.TCP.NoDelay)
	_ = tcpConn.SetKeepAlive(c.TCP.KeepAlive)
}

// NewTLSConfig creates a tls.Config from the TLS section.
// Returns nil if TLS is not configured (plaintext mode).
func (c *File) NewTLSConfig(sni string) (*tls.Config, error) {
	if c.TLS == nil {
		return nil, nil
	}
	tlsCert, err := tls.X509KeyPair([]byte(c.TLS.Certificate), []byte(c.TLS.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("unable to parse certificate: %s", formats.Error(err))
	}
	certPool, err := newX509CertPool(c.TLS.AuthCerts)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		RootCAs:      certPool,
		ServerName:   sni,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// logWrapper wraps slog.Logger to implement io.Writer
type logWrapper struct {
	*slog.Logger
}

func (w *logWrapper) Write(p []byte) (n int, err error) {
	const calldepth = 2
	raw := strings.TrimSuffix(string(p), "\n")
	if msg := strings.TrimPrefix(raw, "[ERR] "); len(msg) != len(raw) {
		w.Println(calldepth, slog.LevelError, nil, msg)
	} else if msg := strings.TrimPrefix(raw, "[WARN] "); len(msg) != len(raw) {
		w.Println(calldepth, slog.LevelWarning, nil, msg)
	} else {
		w.Println(calldepth, slog.LevelError, nil, string(p))
	}
	return len(p), nil
}

// NewMuxConfig creates a yamux.Config from the current configuration.
// MaxHalfOpen is used as the yamux accept backlog. If zero, it defaults to 256.
func (c *File) NewMuxConfig() *yamux.Config {
	acceptBacklog := c.Mux.MaxHalfOpen
	if acceptBacklog <= 0 {
		acceptBacklog = 256
	}
	keepAliveInterval := time.Duration(c.KeepAlive) * time.Second
	enableKeepAlive := keepAliveInterval >= time.Second
	if !enableKeepAlive {
		keepAliveInterval = 15 * time.Second
	}
	openTimeout := time.Duration(c.Mux.StreamOpenTimeout) * time.Second
	if openTimeout <= 0 {
		openTimeout = 30 * time.Second
	}
	closeTimeout := time.Duration(c.Mux.StreamCloseTimeout) * time.Second
	if closeTimeout <= 0 {
		closeTimeout = 120 * time.Second
	}
	return &yamux.Config{
		AcceptBacklog:          acceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      keepAliveInterval,
		ConnectionWriteTimeout: time.Duration(c.SendTimeout) * time.Second,
		MaxStreamWindowSize:    uint32(c.StreamWindow),
		StreamOpenTimeout:      openTimeout,
		StreamCloseTimeout:     closeTimeout,
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}
