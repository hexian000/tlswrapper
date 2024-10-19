package config

import (
	"mime"

	"github.com/hexian000/gosnippets/slog"
)

var (
	mimeType    = "application/x-tlswrapper-config"
	mimeVersion = "3"

	Type = mime.FormatMediaType(mimeType, map[string]string{"version": mimeVersion})
)

// Tunnel represents a "fixed" tunnel between 2 peers
type Tunnel struct {
	// is disabled
	Disabled bool `json:"disabled,omitempty"`
	// mux dial address
	MuxDial string `json:"addr,omitempty"`
	// local listener address
	Listen string `json:"listen,omitempty"`
	// remote service name
	Service string `json:"service,omitempty"`
}

type KeyPair struct {
	// TLS: PEM encoded certificate
	Certificate string `json:"cert"`
	// TLS: PEM encoded private key
	PrivateKey string `json:"key"`
}

type CertPool []string

// File config file
type File struct {
	// type identifier
	Type string `json:"type"`
	// local peer name
	PeerName string `json:"peername,omitempty"`
	// mux listen address
	MuxListen string `json:"muxlisten,omitempty"`
	// service name to dial address
	Services map[string]string `json:"services"`
	// peer name to config
	Peers map[string]Tunnel `json:"peers"`
	// health check and metrics, default to "" (disabled)
	HTTPListen string `json:"httplisten,omitempty"`
	// TLS: local certificates
	Certificates []KeyPair `json:"certs"`
	// TLS: authorized remote certificates, PEM encoded
	AuthorizedCerts CertPool `json:"authcerts"`
	// TCP no delay, default to true
	NoDelay bool `json:"nodelay"`
	// soft limit of concurrent unauthenticated connections, default to 10
	StartupLimitStart int `json:"startuplimitstart"`
	// probability of random disconnection when soft limit is exceeded, default to 30 (30%)
	StartupLimitRate int `json:"startuplimitrate"`
	// hard limit of concurrent unauthenticated connections, default to 60
	StartupLimitFull int `json:"startuplimitfull"`
	// max concurrent streams, default to 16384
	MaxConn int `json:"maxconn"`
	// max concurrent incoming sessions, default to 128
	MaxSessions int `json:"maxsessions"`
	// don't keep tunnels connected, default to false
	NoRedial bool `json:"noredial,omitempty"`
	// client-side keep alive interval in seconds, 0 for disable, default to 25 (every 25s)
	KeepAlive int `json:"keepalive"`
	// server-side keep alive interval in seconds, 0 for disable, default to 300 (every 5min)
	ServerKeepAlive int `json:"serverkeepalive"`
	// mux accept backlog, default to 256, you may not want to change this
	AcceptBacklog int `json:"backlog"`
	// stream window size in bytes, default to 256 KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
	// tunnel connecting timeout in seconds, default to 15
	ConnectTimeout int `json:"timeout"`
	// stream open timeout in seconds, default to 30
	StreamOpenTimeout int `json:"streamopentimeout"`
	// stream close timeout in seconds, default to 120
	StreamCloseTimeout int `json:"streamclosetimeout"`
	// data write request timeout in seconds, default to 15, for detecting network failures earlier
	WriteTimeout int `json:"writetimeout"`
	// log output, default to stdout
	Log string `json:"log"`
	// log output, default to 4 (notice)
	LogLevel slog.Level `json:"loglevel"`
}

var Default = File{
	Type:            Type,
	Services:        map[string]string{},
	Peers:           map[string]Tunnel{},
	Certificates:    []KeyPair{},
	AuthorizedCerts: CertPool{},

	NoDelay:            true,
	StartupLimitStart:  10,
	StartupLimitRate:   30,
	StartupLimitFull:   60,
	MaxConn:            16384,
	MaxSessions:        128,
	KeepAlive:          25,  // every 25s
	ServerKeepAlive:    300, // every 5min
	AcceptBacklog:      256,
	StreamWindow:       256 * 1024, // 256 KiB
	ConnectTimeout:     15,
	StreamOpenTimeout:  30,
	StreamCloseTimeout: 120,
	WriteTimeout:       15,
	Log:                "stdout",
	LogLevel:           slog.LevelNotice,
}
