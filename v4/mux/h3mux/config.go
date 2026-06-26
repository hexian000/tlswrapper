// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"crypto/tls"
	"errors"
	"time"

	"github.com/quic-go/quic-go"
)

// alpn is the Application-Layer Protocol Negotiation identifier for h3mux.
// It must be present in tls.Config.NextProtos on both client and server.
const alpn = "tlswrapper/3"

// Config holds options for creating an h3mux session.
// Zero values for numeric/duration fields use built-in defaults.
type Config struct {
	// LocalID is the local identity claim sent in the handshake.
	LocalID string
	// TLSConfig is required: QUIC mandates TLS 1.3. The h3mux package
	// automatically appends the alpn identifier to NextProtos.
	// For dynamic cert rotation use TLSConfigProvider (it takes precedence).
	TLSConfig *tls.Config
	// TLSConfigProvider, when non-nil, is called to obtain the current TLS
	// config: on each inbound TLS handshake (server side, via
	// GetConfigForClient) and on each Dial (client side). Takes precedence
	// over TLSConfig.
	TLSConfigProvider func() *tls.Config
	// RejectInbound is advertised in the handshake: the peer should not Open() streams to us.
	RejectInbound bool

	// KeepAlivePeriod is how often to send QUIC keepalive pings.
	// 0 uses the QUIC default (disabled unless MaxIdleTimeout is set).
	KeepAlivePeriod time.Duration // default 25s

	// HandshakeTimeout is the QUIC handshake idle timeout.
	// 0 uses quic-go's default of 5s.
	HandshakeTimeout time.Duration

	// MaxIdleTimeout is the maximum time a connection may be idle before being closed.
	// 0 uses quic-go's default of 30s.
	MaxIdleTimeout time.Duration

	// MaxIncomingStreams is the maximum number of inbound bidirectional streams
	// the peer may open concurrently (i.e., the depth of the Accept queue).
	// 0 uses the default of 1024.
	MaxIncomingStreams int64

	// Flow control window sizes. 0 means use quic-go defaults.
	InitialStreamReceiveWindow     uint64
	MaxStreamReceiveWindow         uint64
	InitialConnectionReceiveWindow uint64
	MaxConnectionReceiveWindow     uint64
}

func (c *Config) keepAlivePeriod() time.Duration {
	if c.KeepAlivePeriod > 0 {
		return c.KeepAlivePeriod
	}
	return 25 * time.Second
}

func (c *Config) maxIncomingStreams() int64 {
	if c.MaxIncomingStreams > 0 {
		return c.MaxIncomingStreams
	}
	return 1024
}

// quicConfig builds a *quic.Config from the h3mux Config fields.
func (c *Config) quicConfig() *quic.Config {
	qcfg := &quic.Config{
		KeepAlivePeriod:    c.keepAlivePeriod(),
		MaxIncomingStreams: c.maxIncomingStreams(),
		// Disallow unidirectional streams: h3mux only uses bidirectional streams.
		MaxIncomingUniStreams: -1,
	}
	if c.HandshakeTimeout > 0 {
		qcfg.HandshakeIdleTimeout = c.HandshakeTimeout
	}
	if c.MaxIdleTimeout > 0 {
		qcfg.MaxIdleTimeout = c.MaxIdleTimeout
	}
	if c.InitialStreamReceiveWindow > 0 {
		qcfg.InitialStreamReceiveWindow = c.InitialStreamReceiveWindow
	}
	if c.MaxStreamReceiveWindow > 0 {
		qcfg.MaxStreamReceiveWindow = c.MaxStreamReceiveWindow
	}
	if c.InitialConnectionReceiveWindow > 0 {
		qcfg.InitialConnectionReceiveWindow = c.InitialConnectionReceiveWindow
	}
	if c.MaxConnectionReceiveWindow > 0 {
		qcfg.MaxConnectionReceiveWindow = c.MaxConnectionReceiveWindow
	}
	return qcfg
}

// currentTLSConfig resolves the TLS config to use right now.
// TLSConfigProvider takes precedence over TLSConfig.
func (c *Config) currentTLSConfig() *tls.Config {
	if c.TLSConfigProvider != nil {
		return c.TLSConfigProvider()
	}
	return c.TLSConfig
}

// tlsClientConfig returns a copy of the current TLS config with the h3mux
// ALPN prepended. Panics if no TLS config is available.
func (c *Config) tlsClientConfig() *tls.Config {
	cfg := c.currentTLSConfig().Clone()
	cfg.NextProtos = prependALPN(cfg.NextProtos)
	return cfg
}

// tlsServerConfig returns a copy of the current TLS config with the h3mux
// ALPN prepended. Panics if no TLS config is available.
// When TLSConfigProvider is set, the returned config additionally resolves
// the provider on every inbound handshake via GetConfigForClient, so that
// certificate rotation takes effect without restarting the listener.
func (c *Config) tlsServerConfig() *tls.Config {
	cfg := c.currentTLSConfig().Clone()
	cfg.NextProtos = prependALPN(cfg.NextProtos)
	if c.TLSConfigProvider != nil {
		cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
			cur := c.TLSConfigProvider()
			if cur == nil {
				return nil, errors.New("h3mux: TLS config unavailable")
			}
			cur = cur.Clone()
			cur.NextProtos = prependALPN(cur.NextProtos)
			return cur, nil
		}
	}
	return cfg
}

// prependALPN ensures alpn is the first entry in protos.
func prependALPN(protos []string) []string {
	for _, p := range protos {
		if p == alpn {
			return protos
		}
	}
	return append([]string{alpn}, protos...)
}
