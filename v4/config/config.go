// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"mime"

	"github.com/hexian000/gosnippets/slog"
)

var (
	mimeType    = "application/x-tlswrapper-config"
	mimeVersion = "4"

	// Type is the configuration file type identifier in MIME format
	Type = mime.FormatMediaType(mimeType, map[string]string{"version": mimeVersion})
)

// TLS holds TLS certificate/key material and authorized peer certificates.
// When TLS is nil in the parent File, connections run in plaintext mode.
type TLS struct {
	// PEM certificate (inline PEM or "@path" to read from file at startup)
	Certificate string `json:"cert"`
	// PEM private key (inline PEM or "@path" to read from file at startup)
	PrivateKey string `json:"key"`
	// Authorized peer certificates (inline PEM or "@path" entries)
	AuthCerts []string `json:"authcerts"`
	// SNI sent on outbound TLS handshakes; the peer certificate must be valid
	// for this name. Empty defaults to "example.com", matching the gencerts
	// default server name.
	ServerName string `json:"sni,omitempty"`
}

// Mux holds mux-agnostic transport settings.
// SessionWindow and StreamWindow control the receive window sizes for
// the transport-level flow control. 0 means use the internal default.
type Mux struct {
	// TCP socket options for the mux socket
	TCP TCP `json:"tcp"`
	// Transport-level connection receive window size in bytes (0 = internal default).
	SessionWindow int `json:"session_window"`
	// Transport-level stream receive window size in bytes (0 = internal default).
	StreamWindow int `json:"stream_window"`
	// Historical name for the maximum concurrent streams per mux session.
	// Maps to HTTP/2 MaxConcurrentStreams; 0 disables the explicit limit.
	MaxHalfOpen int `json:"max_halfopen"`
	// Maximum concurrent streams per session (0 = internal default 1024)
	MaxStreams int `json:"max_streams"`
	// Connection establishment timeout in seconds, covering TCP dial, TLS handshake, and mux protocol handshake
	ConnectTimeout int `json:"connect_timeout"`
	// Session ping timeout in seconds
	PingTimeout int `json:"timeout"`
	// Application-level keepalive probe interval in seconds
	KeepAlive int `json:"keepalive"`
	// Mux connection write timeout in seconds; detects stalled links by timing out writes on the underlying connection
	SendTimeout int `json:"send_timeout"`
	// Session idle eviction timeout in seconds (0 = disabled)
	IdleTimeout int `json:"idle_timeout"`
}

// TCP holds TCP socket options.
type TCP struct {
	// Enable TCP keepalive
	KeepAlive bool `json:"keepalive"`
	// Enable TCP_NODELAY
	NoDelay bool `json:"nodelay"`
	// Receive buffer size in bytes (0 = OS default)
	ReadBuffer int `json:"rcvbuf"`
	// Send buffer size in bytes (0 = OS default)
	WriteBuffer int `json:"sndbuf"`
	// Listen backlog for the socket listener
	Backlog int `json:"backlog"`
}

// Identity holds the local handshake identity and per-peer tunnel routing.
type Identity struct {
	// Identity string sent to the peer during the mux handshake
	Claim string `json:"claim,omitempty"`
	// Additional outbound mux dial targets besides the top-level MuxConnect
	MuxConnect []string `json:"mux_connect,omitempty"`
	// Local listen addresses keyed by the remote identity they should use
	Listen map[string]string `json:"listen,omitempty"`
}

// File represents the top-level configuration structure.
type File struct {
	// MIME type identifying the config format and version
	Type string `json:"type"`
	// HTTP management API listen address (empty = disabled)
	APIListen string `json:"api_listen,omitempty"`
	// Address to accept inbound mux connections that create ephemeral tunnels
	MuxListen string `json:"mux_listen,omitempty"`
	// Address for the default config-driven tunnel to dial
	MuxConnect string `json:"mux_connect,omitempty"`
	// Mux protocol to use: "h2mux" (default, gRPC over TCP+TLS) or "h3mux" (QUIC+TLS).
	// h3mux requires TLS to be configured.
	MuxProtocol string `json:"mux_protocol,omitempty"`
	// Local TCP address to accept application traffic on
	Listen string `json:"listen,omitempty"`
	// Forwarding target for streams arriving from inbound ephemeral tunnels
	Connect string `json:"connect,omitempty"`
	// Handshake identity plus per-peer tunnel settings
	Identity Identity `json:"identity,omitempty"`
	// Log output destination ("stdout", "stderr", "syslog", "discard")
	Log string `json:"log,omitempty"`
	// Log verbosity level
	LogLevel slog.Level `json:"loglevel"`
	// Maximum concurrent mux sessions (0 = unlimited)
	MaxSessions int `json:"max_sessions"`
	// Unauthenticated connection throttle in "start:rate:full" format
	MaxStartups string `json:"max_startups,omitempty"`
	// Disable automatic redial for config-driven tunnels with MuxConnect
	NoRedial bool `json:"no_redial,omitempty"`
	// TLS configuration (nil = plaintext mode)
	TLS *TLS `json:"tls,omitempty"`
	// Mux socket and buffer settings
	Mux Mux `json:"mux"`
	// Local TCP socket settings
	TCP TCP `json:"tcp"`
}

// Default holds the baseline configuration with sensible defaults.
var Default = File{
	Type: Type,

	Log:      "stdout",
	LogLevel: slog.LevelNotice,

	MaxSessions: 128,
	MaxStartups: "10:30:60",

	Mux: Mux{
		TCP: TCP{
			KeepAlive: false,
			NoDelay:   true,
			Backlog:   16,
		},
		MaxHalfOpen:    256,
		KeepAlive:      300,
		PingTimeout:    15,
		SendTimeout:    15,
		IdleTimeout:    0,
		MaxStreams:     0,
		ConnectTimeout: 15,
	},
	TCP: TCP{
		KeepAlive: false,
		NoDelay:   true,
		Backlog:   16,
	},
}

// FindListen returns the effective listen address for the given peer name.
// For the empty name "", the top-level Listen field is returned.
func (c *File) FindListen(name string) string {
	if name == "" {
		return c.Listen
	}
	return c.Identity.Listen[name]
}
