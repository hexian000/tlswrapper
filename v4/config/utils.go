// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
)

// SetLogger applies cfg.Log to l.
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

func newX509CertPool(authCerts []string) (*x509.CertPool, error) {
	certPool := x509.NewCertPool()
	for i, cert := range authCerts {
		if !certPool.AppendCertsFromPEM([]byte(cert)) {
			return nil, fmt.Errorf("unable to parse authorized certificate #%d", i)
		}
	}
	return certPool, nil
}

// ConnectTimeout returns the connection establishment timeout as a duration.
func (c *File) ConnectTimeout() time.Duration {
	return time.Duration(c.Mux.ConnectTimeout) * time.Second
}

// PingTimeout returns the session ping timeout as a duration.
func (c *File) PingTimeout() time.Duration {
	return time.Duration(c.Mux.PingTimeout) * time.Second
}

// KeepAlive returns the keepalive probe interval as a duration.
func (c *File) KeepAlive() time.Duration {
	return time.Duration(c.Mux.KeepAlive) * time.Second
}

// SendTimeout returns the mux connection write timeout as a duration.
func (c *File) SendTimeout() time.Duration {
	return time.Duration(c.Mux.SendTimeout) * time.Second
}

// IdleTimeout returns the session idle eviction timeout as a duration.
// A zero return means idle eviction is disabled.
func (c *File) IdleTimeout() time.Duration {
	return time.Duration(c.Mux.IdleTimeout) * time.Second
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
