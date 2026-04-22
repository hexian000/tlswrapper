// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"golang.org/x/net/http2"
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
		NextProtos:   []string{"h2"},
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

// NewH2Server creates an http2.Server configured from the current settings.
// MaxStreams maps to MaxConcurrentStreams; if zero defaults to 256.
func (c *File) NewH2Server() *http2.Server {
	maxStreams := uint32(c.Mux.MaxHalfOpen)
	if maxStreams == 0 {
		maxStreams = 256
	}
	return &http2.Server{
		MaxConcurrentStreams: maxStreams,
		IdleTimeout:          time.Duration(c.IdleTimeout) * time.Second,
	}
}

// NewH2Transport creates an http2.Transport configured from the current settings.
func (c *File) NewH2Transport(tlscfg *tls.Config) *http2.Transport {
	keepAlive := time.Duration(c.KeepAlive) * time.Second
	if keepAlive < time.Second {
		keepAlive = 25 * time.Second
	}
	pingTimeout := time.Duration(c.SessionTimeout) * time.Second
	if pingTimeout < time.Second {
		pingTimeout = 60 * time.Second
	}
	return &http2.Transport{
		TLSClientConfig: tlscfg,
		ReadIdleTimeout: keepAlive,
		PingTimeout:     pingTimeout,
		AllowHTTP:       tlscfg == nil,
	}
}
