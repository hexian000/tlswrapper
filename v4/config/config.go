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

// TLSConfig holds TLS certificate/key material and authorized peer certificates.
// When TLS is nil in the parent File, connections run in plaintext mode.
type TLSConfig struct {
	// PEM certificate (inline PEM or "@path" to read from file at startup)
	Certificate string `json:"cert"`
	// PEM private key (inline PEM or "@path" to read from file at startup)
	PrivateKey string `json:"key"`
	// Colon-separated list of TLS 1.3 ciphersuites (informational; Go stdlib fixes TLS 1.3 suites)
	Ciphersuites string `json:"ciphersuites,omitempty"`
	// Authorized peer certificates (inline PEM or "@path" entries)
	AuthCerts []string `json:"authcerts"`
}

// MuxConfig holds socket and buffer settings for the mux (transport-level) connection.
type MuxConfig struct {
	// Enable SO_REUSEPORT on the mux socket
	ReusePort bool `json:"reuseport"`
	// Enable OS-level TCP keepalive on the mux socket
	KeepAlive bool `json:"keepalive"`
	// Enable TCP_NODELAY on the mux socket
	NoDelay bool `json:"nodelay"`
	// SO_SNDBUF hint in bytes (0 = system default)
	SndBuf int `json:"sndbuf,omitempty"`
	// SO_RCVBUF hint in bytes (0 = system default)
	RcvBuf int `json:"rcvbuf,omitempty"`
	// Listen backlog for the mux socket
	Backlog int `json:"backlog"`
	// Maximum half-open streams per session (0 = unlimited)
	MaxHalfOpen int `json:"max_halfopen"`
	// Per-stream receive buffer size in bytes (flow-control window)
	ReadMem int `json:"rmem"`
	// Per-stream send buffer size in bytes
	WriteMem int `json:"wmem"`
	// Session memory pressure threshold in bytes (0 = auto)
	MemPressure int64 `json:"mem_pressure"`
	// Stream open timeout in seconds
	StreamOpenTimeout int `json:"stream_open_timeout,omitempty"`
	// Stream close timeout in seconds
	StreamCloseTimeout int `json:"stream_close_timeout,omitempty"`
}

// TCPConfig holds socket options for local (application-side) TCP connections.
type TCPConfig struct {
	// Enable SO_REUSEPORT on local sockets
	ReusePort bool `json:"reuseport"`
	// Enable TCP keepalive on local sockets
	KeepAlive bool `json:"keepalive"`
	// Enable TCP_NODELAY on local sockets
	NoDelay bool `json:"nodelay"`
	// SO_SNDBUF hint in bytes (0 = system default)
	SndBuf int `json:"sndbuf,omitempty"`
	// SO_RCVBUF hint in bytes (0 = system default)
	RcvBuf int `json:"rcvbuf,omitempty"`
	// Listen backlog for the local listener
	Backlog int `json:"backlog"`
}

// ServiceEntry configures routing for a single named service.
type ServiceEntry struct {
	// Local TCP address to accept application traffic on for this service
	Listen string `json:"listen,omitempty"`
	// Mux endpoint address to dial for this service
	MuxConnect string `json:"mux_connect,omitempty"`
	// Forwarding target for inbound streams tagged with this service ID
	Connect string `json:"connect,omitempty"`
}

// File represents the top-level configuration structure.
type File struct {
	// MIME type identifying the config format and version
	Type string `json:"type"`
	// Self identity announced in the handshake
	ID string `json:"id,omitempty"`
	// HTTP management API listen address (empty = disabled)
	APIListen string `json:"api_listen,omitempty"`
	// Address to accept inbound mux connections (server mode)
	MuxListen string `json:"mux_listen,omitempty"`
	// Address to dial for the outbound mux connection (client mode)
	MuxConnect string `json:"mux_connect,omitempty"`
	// Local TCP address to accept application traffic on
	Listen string `json:"listen,omitempty"`
	// Forwarding target for inbound application streams
	Connect string `json:"connect,omitempty"`
	// Session inactivity timeout in seconds
	SessionTimeout int `json:"timeout"`
	// Application-level keepalive probe interval in seconds
	KeepAlive int `json:"keepalive"`
	// Send completion timeout in seconds
	SendTimeout int `json:"send_timeout"`
	// Session idle eviction timeout in seconds (0 = disabled)
	IdleTimeout int `json:"idle_timeout"`
	// Log output destination ("stdout", "stderr", "syslog", "discard")
	Log string `json:"log,omitempty"`
	// Log verbosity level
	LogLevel slog.Level `json:"loglevel"`
	// Maximum concurrent mux sessions (0 = unlimited)
	MaxSessions int `json:"max_sessions"`
	// Maximum concurrent streams per session (0 = internal default 1024)
	MaxStreams int `json:"max_streams"`
	// Convenience alias: sets Mux.WriteMem=X and Mux.ReadMem=2X (0 = no override)
	StreamWindow int `json:"stream_window,omitempty"`
	// Unauthenticated connection throttle in "start:rate:full" format
	MaxStartups string `json:"max_startups,omitempty"`
	// Disable automatic tunnel redial (client mode)
	NoRedial bool `json:"no_redial,omitempty"`
	// TLS configuration (nil = plaintext mode)
	TLS *TLSConfig `json:"tls,omitempty"`
	// Mux socket and buffer settings
	Mux MuxConfig `json:"mux"`
	// Local TCP socket settings
	TCP TCPConfig `json:"tcp"`
	// Named-service routing entries keyed by service ID
	Service map[string]ServiceEntry `json:"service,omitempty"`
}

// Default holds the baseline configuration with sensible defaults.
var Default = File{
	Type: Type,

	SessionTimeout: 60,
	KeepAlive:      25,
	SendTimeout:    15,
	IdleTimeout:    0,

	Log:      "stdout",
	LogLevel: slog.LevelNotice,

	MaxSessions: 128,
	MaxStreams:  0,
	MaxStartups: "10:30:60",

	Mux: MuxConfig{
		NoDelay:            true,
		Backlog:            16,
		MaxHalfOpen:        256,
		ReadMem:            262144, // 256 KiB
		WriteMem:           131072, // 128 KiB
		StreamOpenTimeout:  30,
		StreamCloseTimeout: 120,
	},
	TCP: TCPConfig{
		KeepAlive: true,
		NoDelay:   true,
		Backlog:   16,
	},
}
