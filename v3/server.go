package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/net/hlistener"
	"github.com/hexian000/gosnippets/routines"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3/eventlog"
	"github.com/hexian000/tlswrapper/v3/forwarder"
)

const network = "tcp"

var (
	ErrNoDialAddress  = errors.New("no dial address is configured")
	ErrDialInProgress = errors.New("another dial is in progress")
)

// Server object
type Server struct {
	c      *Config
	tlscfg *tls.Config
	cfgMu  sync.RWMutex

	l hlistener.Listener
	f forwarder.Forwarder

	flowStats    *snet.FlowStats
	recentEvents eventlog.Recent

	listeners map[string]net.Listener
	peers     map[string]*Tunnel // map[service]tunnel
	peersMu   sync.RWMutex
	ctx       contextMgr

	dialer net.Dialer
	g      routines.Group

	numSessions atomic.Uint32
	started     time.Time

	stats struct {
		authorized atomic.Uint64
		request    atomic.Uint64
		success    atomic.Uint64
	}
}

// NewServer creates a server object
func NewServer(cfg *Config) *Server {
	g := routines.NewGroup()
	return &Server{
		listeners: make(map[string]net.Listener),
		peers:     make(map[string]*Tunnel),
		ctx: contextMgr{
			timeout:  cfg.Timeout,
			contexts: make(map[context.Context]context.CancelFunc),
		},
		f:            forwarder.New(cfg.MaxConn, g),
		flowStats:    &snet.FlowStats{},
		recentEvents: eventlog.NewRecent(100),
		g:            g,
		c:            cfg,
	}
}

func (s *Server) addPeer(name string, c *TunnelConfig) *Tunnel {
	t := NewTunnel(s, name, c)
	s.peersMu.Lock()
	defer s.peersMu.Unlock()
	s.peers[name] = t
	return t
}

func (s *Server) findPeer(name string) *Tunnel {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	return s.peers[name]
}

func (s *Server) getAllTunnels() []*Tunnel {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	tunnels := make([]*Tunnel, 0, len(s.peers))
	for _, t := range s.peers {
		tunnels = append(tunnels, t)
	}
	return tunnels
}

type ServerStats struct {
	NumSessions int
	NumStreams  int
	Rx, Tx      uint64
	Accepted    uint64
	Served      uint64
	Authorized  uint64
	ReqTotal    uint64
	ReqSuccess  uint64
	tunnels     []TunnelStats
}

func (s *Server) Stats() (stats ServerStats) {
	if s.l != nil {
		stats.Accepted, stats.Served = s.l.Stats()
	}
	for _, t := range s.getAllTunnels() {
		tstats := t.Stats()
		stats.NumSessions += tstats.NumSessions
		stats.NumStreams += tstats.NumStreams
		stats.tunnels = append(stats.tunnels, tstats)
	}
	stats.Rx, stats.Tx = s.flowStats.Read.Load(), s.flowStats.Written.Load()
	stats.Authorized = s.stats.authorized.Load()
	stats.ReqTotal, stats.ReqSuccess = s.stats.request.Load(), s.stats.success.Load()
	return
}

func (s *Server) dialDirect(ctx context.Context, addr string) (net.Conn, error) {
	slog.Verbose("forward to: ", addr)
	dialed, err := s.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return dialed, nil
}

func (s *Server) serveOne(accepted net.Conn, handler Handler) {
	defer func() {
		if r := recover(); r != nil {
			slog.Errorf("panic: %v\n%s", r, string(debug.Stack()))
		}
	}()
	ctx := s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer s.ctx.cancel(ctx)
	handler.Serve(ctx, accepted)
}

func (s *Server) Serve(listener net.Listener, handler Handler) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, io.EOF) ||
				errors.Is(err, net.ErrClosed) ||
				errors.Is(err, yamux.ErrSessionShutdown) {
				return
			}
			slog.Errorf("serve: %s", formats.Error(err))
			return
		}
		s.serveOne(conn, handler)
	}
}

func (s *Server) Listen(addr string) (net.Listener, error) {
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Error("listen ", addr, ": ", formats.Error(err))
		return listener, err
	}
	slog.Info("listen: ", listener.Addr())
	s.listeners[addr] = listener
	return listener, err
}

// Start the service
func (s *Server) Start() error {
	if s.c.MuxListen != "" {
		l, err := s.Listen(s.c.MuxListen)
		if err != nil {
			return err
		}
		slog.Noticef("mux listen: %v", l.Addr())
		h := &TLSHandler{s: s}
		c := s.getConfig()
		s.l = hlistener.Wrap(l, &hlistener.Config{
			Start:       uint32(c.StartupLimitStart),
			Full:        uint32(c.StartupLimitFull),
			Rate:        float64(c.StartupLimitRate) / 100.0,
			MaxSessions: uint32(c.MaxSessions),
			Stats:       h.Stats4Listener,
		})
		if err := s.g.Go(func() {
			s.Serve(s.l, h)
		}); err != nil {
			return err
		}
	}
	if s.c.HTTPListen != "" {
		l, err := s.Listen(s.c.HTTPListen)
		if err != nil {
			return err
		}
		slog.Noticef("http listen: %v", l.Addr())
		if err := s.g.Go(func() {
			if err := RunHTTPServer(l, s); err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Error(formats.Error(err))
			}
		}); err != nil {
			return err
		}
	}
	for name, c := range s.c.Peers {
		t := s.addPeer(name, &c)
		slog.Debugf("tunnel %q: start", name)
		if err := t.Start(); err != nil {
			return err
		}
	}
	s.started = time.Now()
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	for addr, listener := range s.listeners {
		slog.Info("listener close: ", listener.Addr())
		ioClose(listener)
		delete(s.listeners, addr)
	}
	s.ctx.close()
	s.f.Close()
	s.g.Close()
	slog.Info("waiting for unfinished connections")
	s.g.Wait()
	return nil
}

// LoadConfig reloads the configuration file
func (s *Server) LoadConfig(cfg *Config) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.c != nil {
		cfg.Services = s.c.Services
	}
	tlscfg, err := cfg.NewTLSConfig(cfg.ServerName)
	if err != nil {
		return err
	}
	s.c = cfg
	s.tlscfg = tlscfg
	return nil
}

func (s *Server) getConfig() *Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.c
}

func (s *Server) getTLSConfig() *tls.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.tlscfg
}
