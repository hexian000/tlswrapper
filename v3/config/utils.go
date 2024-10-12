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

func (c *KeyPair) Load() error {
	if c.CertPEM == "" {
		certPEMBlock, err := os.ReadFile(c.Certificate)
		if err != nil {
			err := fmt.Errorf("read certificate %s: %s", c.Certificate, formats.Error(err))
			return err
		}
		c.CertPEM = string(certPEMBlock)
	}
	if c.KeyPEM == "" {
		keyPEMBlock, err := os.ReadFile(c.PrivateKey)
		if err != nil {
			err := fmt.Errorf("read private key %s: %s", c.PrivateKey, formats.Error(err))
			return err
		}
		c.KeyPEM = string(keyPEMBlock)
	}
	return nil
}

func (p CertPool) Load() error {
	for i, c := range p {
		if c.CertPEM == "" {
			certPEMBlock, err := os.ReadFile(c.Certificate)
			if err != nil {
				err := fmt.Errorf("read certificate %s: %s", c.Certificate, formats.Error(err))
				return err
			}
			c.CertPEM = string(certPEMBlock)
		}
		p[i] = c
	}
	return nil
}

func (p CertPool) NewX509CertPool() (*x509.CertPool, error) {
	certPool := x509.NewCertPool()
	for i, c := range p {
		if !certPool.AppendCertsFromPEM([]byte(c.CertPEM)) {
			err := fmt.Errorf("unable to parse authorized certificate #%d", i)
			return nil, err
		}
	}
	return certPool, nil
}

// GetTunnel finds the tunnel config
func (c *File) GetTunnel(peerName string) *Tunnel {
	tuncfg, ok := c.Peers[peerName]
	if !ok {
		return nil
	}
	return &tuncfg
}

// Timeout gets the generic request timeout
func (c *File) Timeout() time.Duration {
	return time.Duration(c.ConnectTimeout) * time.Second
}

// SetConnParams sets TCP params
func (c *File) SetConnParams(conn net.Conn) {
	if tcpConn := conn.(*net.TCPConn); tcpConn != nil {
		_ = tcpConn.SetNoDelay(c.NoDelay)
		_ = tcpConn.SetKeepAlive(false) // we have an encrypted one
	}
}

// NewTLSConfig creates tls.Config
func (c *File) NewTLSConfig() (*tls.Config, error) {
	sni := c.ServerName
	certs := make([]tls.Certificate, 0, len(c.Certificates))
	for i, cert := range c.Certificates {
		tlsCert, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(cert.KeyPEM))
		if err != nil {
			err := fmt.Errorf("unable to parse certificate #%d: %s", i, formats.Error(err))
			return nil, err
		}
		certs = append(certs, tlsCert)
	}
	certPool, err := c.AuthorizedCerts.NewX509CertPool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: certs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		RootCAs:      certPool,
		ServerName:   sni,
		MinVersion:   tls.VersionTLS13,
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
func (c *File) NewMuxConfig(peerName string, isDialed bool) *yamux.Config {
	acceptBacklog := c.AcceptBacklog
	streamWindow := c.StreamWindow
	keepAlive := c.ServerKeepAlive
	if isDialed {
		keepAlive = c.KeepAlive
	}
	if t, ok := c.Peers[peerName]; ok {
		acceptBacklog = t.AcceptBacklog
		streamWindow = t.StreamWindow
		if isDialed {
			keepAlive = t.KeepAlive
		}
	}

	keepAliveInterval := time.Duration(keepAlive) * time.Second
	enableKeepAlive := keepAliveInterval >= time.Second
	if !enableKeepAlive {
		keepAliveInterval = 15 * time.Second
	}
	return &yamux.Config{
		AcceptBacklog:          acceptBacklog,
		EnableKeepAlive:        enableKeepAlive,
		KeepAliveInterval:      keepAliveInterval,
		ConnectionWriteTimeout: time.Duration(c.WriteTimeout) * time.Second,
		MaxStreamWindowSize:    streamWindow,
		StreamOpenTimeout:      time.Duration(c.StreamOpenTimeout) * time.Second,
		StreamCloseTimeout:     time.Duration(c.StreamCloseTimeout) * time.Second,
		Logger:                 log.New(&logWrapper{slog.Default()}, "", 0),
	}
}
