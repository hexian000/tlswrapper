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
	"github.com/hexian000/tlswrapper/v3/config"
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
	cfg    *config.File
	tlscfg *tls.Config
	cfgMu  sync.RWMutex

	l           hlistener.Listener
	apiListener net.Listener
	f           forwarder.Forwarder

	flowStats    *snet.FlowStats
	recentEvents eventlog.Recent

	tunnels   map[string]*tunnel // map[service]tunnel
	tunnelsMu sync.RWMutex
	mux       map[*yamux.Session]string // map[mux]tag
	muxMu     sync.RWMutex
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
func NewServer(cfg *config.File) (*Server, error) {
	g := routines.NewGroup()
	s := &Server{
		cfg:     cfg,
		tunnels: make(map[string]*tunnel),
		mux:     make(map[*yamux.Session]string),
		ctx: contextMgr{
			contexts: make(map[context.Context]context.CancelFunc),
		},
		f:            forwarder.New(cfg.MaxConn, g),
		flowStats:    &snet.FlowStats{},
		recentEvents: eventlog.NewRecent(100),
		g:            g,
	}
	s.ctx.timeout = func() time.Duration {
		cfg, _ := s.getConfig()
		return cfg.Timeout()
	}
	tlscfg, err := cfg.NewTLSConfig(Flags.ServerName)
	if err != nil {
		return nil, err
	}
	s.tlscfg = tlscfg
	return s, err
}

func (s *Server) findTunnel(peerName string) *tunnel {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()
	return s.tunnels[peerName]
}

func (s *Server) getAllTunnels() []*tunnel {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()
	tunnels := make([]*tunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		tunnels = append(tunnels, t)
	}
	return tunnels
}

func (s *Server) stopAllTunnels() {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()
	for _, t := range s.tunnels {
		_ = t.Stop()
	}
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
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				time.Sleep(500 * time.Millisecond)
			}
			slog.Errorf("serve: %s", formats.Error(err))
			return
		}
		if err := s.g.Go(func() {
			s.serveOne(conn, handler)
		}); err != nil {
			slog.Errorf("serve: %s", formats.Error(err))
		}
	}
}

func (s *Server) addMux(mux *yamux.Session, tag string) {
	slog.Infof("%s: mux established", tag)
	s.muxMu.Lock()
	defer s.muxMu.Unlock()
	s.mux[mux] = tag
}

func (s *Server) getMuxTag(mux *yamux.Session) (string, bool) {
	s.muxMu.RLock()
	defer s.muxMu.RUnlock()
	tag, ok := s.mux[mux]
	return tag, ok
}

func (s *Server) delMux(mux *yamux.Session) {
	if tag, ok := s.getMuxTag(mux); ok {
		slog.Infof("%s: mux connection lost", tag)
	}
	s.muxMu.Lock()
	defer s.muxMu.Unlock()
	delete(s.mux, mux)
}

func (s *Server) startMux(conn net.Conn, cfg *config.File, peerName, service string, t *tunnel, tag string) (*yamux.Session, error) {
	muxcfg := cfg.NewMuxConfig(peerName, t != nil)
	handshakeFunc := yamux.Server
	if t != nil {
		handshakeFunc = yamux.Client
	}
	mux, err := handshakeFunc(conn, muxcfg)
	if err != nil {
		ioClose(conn)
		return nil, err
	}
	serveFunc := func() {
		s.addMux(mux, tag)
		defer s.delMux(mux)
		s.Serve(mux, &ForwardHandler{s, peerName, service})
	}
	if peerTun := s.findTunnel(peerName); peerTun != nil {
		serveFunc = func() {
			peerTun.addMux(mux, tag)
			defer peerTun.delMux(mux)
			s.Serve(mux, &ForwardHandler{s, peerName, service})
		}
	}
	if err := s.g.Go(serveFunc); err != nil {
		ioClose(mux)
		return nil, err
	}
	return mux, nil
}

func (s *Server) Listen(addr string) (net.Listener, error) {
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Errorf("listen %s: %s", addr, formats.Error(err))
		return listener, err
	}
	slog.Infof("listen: %v", listener.Addr())
	return listener, err
}

func (s *Server) Unlisten(listener net.Listener) {
	ioClose(listener)
	slog.Infof("listener close: %v", listener.Addr())
}

func (s *Server) reloadTunnels(cfg *config.File) error {
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	for name, t := range s.tunnels {
		if _, ok := cfg.Peers[name]; !ok {
			if err := t.Stop(); err != nil {
				slog.Errorf("tunnel %q: %s", name, formats.Error(err))
			}
			delete(s.tunnels, name)
		}
	}
	for name := range cfg.Peers {
		t := &tunnel{
			peerName: name, s: s,
			mux:       make(map[*yamux.Session]string),
			closeSig:  make(chan struct{}, 1),
			redialSig: make(chan struct{}, 1),
		}
		s.tunnels[name] = t
		if err := t.Start(); err != nil {
			return err
		}
	}
	return nil
}

// Start the service
func (s *Server) Start() error {
	if s.cfg.MuxListen != "" {
		l, err := s.Listen(s.cfg.MuxListen)
		if err != nil {
			return err
		}
		slog.Noticef("mux listen: %v", l.Addr())
		h := &TLSHandler{s: s}
		c, _ := s.getConfig()
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
			ioClose(s.l)
			s.l = nil
			return err
		}
	}
	if s.cfg.HTTPListen != "" {
		l, err := s.Listen(s.cfg.HTTPListen)
		if err != nil {
			return err
		}
		slog.Noticef("http listen: %v", l.Addr())
		if err := s.g.Go(func() {
			if err := RunHTTPServer(l, s); err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Error(formats.Error(err))
			}
		}); err != nil {
			ioClose(l)
			return err
		}
		s.apiListener = l
	}
	if err := s.reloadTunnels(s.cfg); err != nil {
		return err
	}
	s.started = time.Now()
	return nil
}

func (s *Server) closeAllMux() {
	s.muxMu.Lock()
	defer s.muxMu.Unlock()
	for mux := range s.mux {
		ioClose(mux)
		delete(s.mux, mux)
	}
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	if s.l != nil {
		ioClose(s.l)
		s.l = nil
	}
	if s.apiListener != nil {
		ioClose(s.apiListener)
		s.apiListener = nil
	}
	s.ctx.close()
	s.stopAllTunnels()
	s.closeAllMux()
	s.f.Close()
	s.g.Close()
	slog.Info("waiting for unfinished connections")
	s.g.Wait()
	return nil
}

// LoadConfig reloads the configuration file
func (s *Server) LoadConfig(cfg *config.File) error {
	tlscfg, err := cfg.NewTLSConfig(Flags.ServerName)
	if err != nil {
		return err
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
	s.tlscfg = tlscfg
	return nil
}

func (s *Server) getConfig() (*config.File, *tls.Config) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg, s.tlscfg
}
