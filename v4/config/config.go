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
}

// Mux holds socket and buffer settings for the mux (transport-level) connection.
type Mux struct {
	// Enable OS-level TCP keepalive on the mux socket
	KeepAlive bool `json:"keepalive"`
	// Enable TCP_NODELAY on the mux socket
	NoDelay bool `json:"nodelay"`
	// Listen backlog for the mux socket
	Backlog int `json:"backlog"`
	// Maximum concurrent streams per connection; maps to HTTP/2 MaxConcurrentStreams (0 = default 256)
	MaxHalfOpen int `json:"max_halfopen"`
}

// TCP holds socket options for local (application-side) TCP connections.
type TCP struct {
	// Enable TCP keepalive on local sockets
	KeepAlive bool `json:"keepalive"`
	// Enable TCP_NODELAY on local sockets
	NoDelay bool `json:"nodelay"`
	// Listen backlog for the local listener
	Backlog int `json:"backlog"`
}

type Service struct {
	// Self identity announced in the handshake
	ID string `json:"id,omitempty"`
	// Peer identity to mux dial address mapping
	Peers map[string]string `json:"peers,omitempty"`
	// Peer identity to local listen address mapping
	Listen map[string]string `json:"listen,omitempty"`
}

// ServiceEntry is the effective routing entry for one peer/session.
type ServiceEntry struct {
	Listen string
	// Address to dial for outbound mux session.
	MuxConnect string
	// Forwarding target for inbound application streams.
	Connect string
}

// File represents the top-level configuration structure.
type File struct {
	// MIME type identifying the config format and version
	Type string `json:"type"`
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
	// Service identity and per-peer routing settings
	Service Service `json:"service,omitempty"`
	// Application-level keepalive probe interval in seconds
	KeepAlive int `json:"keepalive"`
	// Session ping timeout in seconds
	PingTimeout int `json:"timeout"`
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
	// Unauthenticated connection throttle in "start:rate:full" format
	MaxStartups string `json:"max_startups,omitempty"`
	// Disable automatic session redial (client mode)
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

	KeepAlive:   25,
	PingTimeout: 15,
	SendTimeout: 15,
	IdleTimeout: 0,

	Log:      "stdout",
	LogLevel: slog.LevelNotice,

	MaxSessions: 128,
	MaxStreams:  0,
	MaxStartups: "10:30:60",

	Mux: Mux{
		NoDelay:     true,
		Backlog:     16,
		MaxHalfOpen: 256,
	},
	TCP: TCP{
		KeepAlive: false,
		NoDelay:   true,
		Backlog:   16,
	},
}

// ServiceEntry returns the effective ServiceEntry for the given peer name.
// For the empty name "", the top-level Listen/MuxConnect/Connect fields are used
// as the default unnamed service.
func (c *File) ServiceEntry(name string) ServiceEntry {
	entry := ServiceEntry{Connect: c.Connect}
	if name == "" {
		entry.Listen = c.Listen
		entry.MuxConnect = c.MuxConnect
		return entry
	}
	entry.Listen = c.Service.Listen[name]
	entry.MuxConnect = c.Service.Peers[name]
	return entry
}
