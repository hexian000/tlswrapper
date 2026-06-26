// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"crypto/tls"
	"math"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Config holds options for creating an h2mux session.
// Zero values for numeric/duration fields use built-in defaults.
type Config struct {
	// LocalID is the local identity claim sent in the handshake.
	LocalID string
	// TLSConfig, when non-nil, causes Client/Server to perform a TLS handshake
	// on the raw connection before starting gRPC. nil means plaintext.
	// For dynamic cert rotation use TLSConfigProvider (it takes precedence).
	TLSConfig *tls.Config
	// TLSConfigProvider, when non-nil, is called on each Dial/AcceptSession to
	// obtain the current TLS config. Takes precedence over TLSConfig.
	TLSConfigProvider func() *tls.Config
	// RejectInbound is advertised in the hello: the peer should not Open() streams to us.
	RejectInbound bool

	// Dialer is used by H2Mux.Dial to establish outbound TCP connections.
	// The zero value (net.Dialer{}) uses the system default.
	Dialer net.Dialer
	// ConnSetup, when non-nil, is called on each accepted or dialed net.Conn
	// immediately after the TCP connection is established and before the mux
	// handshake.  Use it to apply socket options such as TCP_NODELAY.
	ConnSetup func(net.Conn)

	// Client-side transport tuning.
	KeepAlive     time.Duration // default 25s
	PingTimeout   time.Duration // default 15s
	WriteTimeout  time.Duration // connection-level write timeout on the underlying net.Conn; 0 disables it
	SessionWindow int32         // 0 = gRPC dynamic flow control
	StreamWindow  int32         // 0 = gRPC dynamic flow control

	// Server-side listener tuning.
	MaxConcurrentStreams uint32        // default 256
	IdleTimeout          time.Duration // default 0 (no idle timeout)
}

// tlsConfig resolves the TLS config to use for a single connection.
// TLSConfigProvider takes precedence over TLSConfig.
func (c *Config) tlsConfig() *tls.Config {
	if c.TLSConfigProvider != nil {
		return c.TLSConfigProvider()
	}
	return c.TLSConfig
}

func windowSize(v int32) int32 {
	if v >= 65535 {
		return v
	}
	return 0
}

func (c *Config) sessionWindow() int32 { return windowSize(c.SessionWindow) }

func (c *Config) streamWindow() int32 { return windowSize(c.StreamWindow) }

func (c *Config) keepAlive() time.Duration {
	if c.KeepAlive > 0 {
		return c.KeepAlive
	}
	return 25 * time.Second
}

func (c *Config) pingTimeout() time.Duration {
	if c.PingTimeout > 0 {
		return c.PingTimeout
	}
	return 15 * time.Second
}

func (c *Config) maxConcurrentStreams() uint32 {
	if c.MaxConcurrentStreams > 0 {
		return c.MaxConcurrentStreams
	}
	return math.MaxUint32 - 1 // reserve 1 for the control stream
}

// connBufferSize is the gRPC transport read/write buffer size. The default
// (32 KiB) flushes every other chunk under bulk transfer; batching more
// frames per syscall measurably reduces write-side CPU. Data is only
// buffered while the kernel socket is busy, so latency is unaffected.
const connBufferSize = 128 * 1024

func (c *Config) grpcDialOptions() []grpc.DialOption {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                c.keepAlive(),
			Timeout:             c.pingTimeout(),
			PermitWithoutStream: true,
		}),
		grpc.WithDisableRetry(),
		grpc.WithDisableServiceConfig(),
		grpc.WithReadBufferSize(connBufferSize),
		grpc.WithWriteBufferSize(connBufferSize),
	}
	if window := c.sessionWindow(); window > 0 {
		opts = append(opts, grpc.WithStaticConnWindowSize(window))
	}
	if window := c.streamWindow(); window > 0 {
		opts = append(opts, grpc.WithStaticStreamWindowSize(window))
	}
	return opts
}

func (c *Config) grpcServerOptions() []grpc.ServerOption {
	opts := []grpc.ServerOption{
		grpc.Creds(insecure.NewCredentials()),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:              c.keepAlive(),
			Timeout:           c.pingTimeout(),
			MaxConnectionIdle: c.IdleTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             1 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.MaxConcurrentStreams(c.maxConcurrentStreams()),
		grpc.ReadBufferSize(connBufferSize),
		grpc.WriteBufferSize(connBufferSize),
	}
	if window := c.sessionWindow(); window > 0 {
		opts = append(opts, grpc.StaticConnWindowSize(window))
	}
	if window := c.streamWindow(); window > 0 {
		opts = append(opts, grpc.StaticStreamWindowSize(window))
	}
	return opts
}
