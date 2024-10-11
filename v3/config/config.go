package config

import "github.com/hexian000/gosnippets/slog"

// Tunnel represents a "fixed" tunnel between 2 peers
type Tunnel struct {
	// (optional) is disabled
	Disabled bool `json:"disabled,omitempty"`
	// (optional) mux dial address
	MuxDial string `json:"addr,omitempty"`
	// (optional) local listener address
	Listen string `json:"listen,omitempty"`
	// remote service name
	PeerService string `json:"peerservice"`
	// (optional) true for overwritting the global value
	NoRedial bool `json:"noredial"`
	// (optional) non-zero for overwritting the global value
	KeepAlive int `json:"keepalive"`
	// (optional) non-zero for overwritting the global value
	AcceptBacklog int `json:"backlog"`
	// (optional) non-zero for overwritting the global value
	StreamWindow uint32 `json:"window"`
}

// File config file
type File struct {
	// (optional) local peer name
	PeerName string `json:"peername,omitempty"`
	// (optional) mux listen address
	MuxListen string `json:"muxlisten,omitempty"`
	// service name to dial address
	Services map[string]string `json:"services"`
	// peer name to config
	Peers map[string]Tunnel `json:"peers"`
	// (optional) health check and metrics, default to "" (disabled)
	HTTPListen string `json:"httplisten,omitempty"`
	// TLS: (optional) SNI field in handshake, default to "example.com"
	ServerName string `json:"sni"`
	// TLS: local certificate
	Certificate string `json:"cert"`
	// TLS: local private key
	PrivateKey string `json:"key"`
	// TLS: authorized remote certificates, bundle supported
	AuthorizedCerts []string `json:"authcerts"`
	// (optional) TCP no delay, default to true
	NoDelay bool `json:"nodelay"`
	// (optional) soft limit of concurrent unauthenticated connections, default to 10
	StartupLimitStart int `json:"startuplimitstart"`
	// (optional) probability of random disconnection when soft limit is exceeded, default to 30 (30%)
	StartupLimitRate int `json:"startuplimitrate"`
	// (optional) hard limit of concurrent unauthenticated connections, default to 60
	StartupLimitFull int `json:"startuplimitfull"`
	// (optional) max concurrent streams, default to 16384
	MaxConn int `json:"maxconn"`
	// (optional) max concurrent incoming sessions, default to 128
	MaxSessions int `json:"maxsessions"`
	// (optional) don't keep tunnels connected, default to false
	NoRedial bool `json:"noredial"`
	// (optional) client-side keep alive interval in seconds, 0 for disable, default to 25 (every 25s)
	KeepAlive int `json:"keepalive"`
	// (optional) server-side keep alive interval in seconds, 0 for disable, default to 300 (every 5min)
	ServerKeepAlive int `json:"serverkeepalive"`
	// (optional) mux accept backlog, default to 256, you may not want to change this
	AcceptBacklog int `json:"backlog"`
	// (optional) stream window size in bytes, default to 256 KiB, increase this on long fat networks
	StreamWindow uint32 `json:"window"`
	// (optional) tunnel connecting timeout in seconds, default to 15
	ConnectTimeout int `json:"timeout"`
	// (optional) stream open timeout in seconds, default to 30
	StreamOpenTimeout int `json:"streamopentimeout"`
	// (optional) stream close timeout in seconds, default to 120
	StreamCloseTimeout int `json:"streamclosetimeout"`
	// (optional) data write request timeout in seconds, default to 15, for detecting network failures earlier
	WriteTimeout int `json:"writetimeout"`
	// (optional) log output, default to stdout
	Log string `json:"log,omitempty"`
	// (optional) log output, default to 4 (notice)
	LogLevel slog.Level `json:"loglevel"`
}

var Default = File{
	ServerName:         "example.com",
	NoDelay:            true,
	StartupLimitStart:  10,
	StartupLimitRate:   30,
	StartupLimitFull:   60,
	MaxConn:            16384,
	MaxSessions:        128,
	NoRedial:           true,
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
