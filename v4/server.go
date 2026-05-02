// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/net/hlistener"
	"github.com/hexian000/gosnippets/routines"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/eventlog"
	"github.com/hexian000/tlswrapper/v4/forwarder"
	"github.com/hexian000/tlswrapper/v4/mux"
)

const network = "tcp"

var (
	ErrNoDialAddress  = errors.New("no dial address is configured")
	ErrDialInProgress = errors.New("another dial is in progress")
	ErrNoSession      = errors.New("no active session")
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

	mu       sync.RWMutex
	services map[string]*tunnel // config-driven tunnels keyed by peer service ID
	sessions []*tunnel          // all tunnels, including config-driven and inbound ephemeral
	ctx      contextMgr

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
		cfg:      cfg,
		services: make(map[string]*tunnel),
		ctx: contextMgr{
			contexts: make(map[context.Context]context.CancelFunc),
		},
		f:            forwarder.New(maxStreams(cfg), g),
		flowStats:    &snet.FlowStats{},
		recentEvents: eventlog.NewRecent(100),
		g:            g,
	}
	s.ctx.timeout = func() time.Duration {
		cfg, _ := s.getConfig()
		return cfg.Timeout()
	}
	tlscfg, err := cfg.NewTLSConfig(appFlags.ServerName)
	if err != nil {
		return nil, err
	}
	s.tlscfg = tlscfg
	return s, nil
}

func maxStreams(cfg *config.File) int {
	if cfg.MaxStreams == 0 {
		return 1024
	}
	return cfg.MaxStreams
}

// findSession returns the first active tunnel for the given peer service ID.
func (s *Server) findSession(peerServiceId string) *tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ss := range s.sessions {
		if ss.id == peerServiceId && ss.getSession() != nil {
			return ss
		}
	}
	return nil
}

func (s *Server) getAllSessions() []*tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*tunnel, len(s.sessions))
	copy(result, s.sessions)
	return result
}

func (s *Server) addSession(ss *tunnel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, ss)
}

func (s *Server) removeSession(target *tunnel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ss := range s.sessions {
		if ss == target {
			s.sessions = append(s.sessions[:i], s.sessions[i+1:]...)
			return
		}
	}
}

// ServerStats holds statistics of the server
type ServerStats struct {
	NumSessions int
	NumStreams  int
	Rx, Tx      uint64
	Accepted    uint64
	Served      uint64
	Authorized  uint64
	ReqTotal    uint64
	ReqSuccess  uint64
	sessions    []SessionStats
}

// Stats returns the current server statistics
func (s *Server) Stats() (stats ServerStats) {
	if s.l != nil {
		stats.Accepted, stats.Served = s.l.Stats()
	}
	sessionMap := make(map[string]SessionStats)
	for _, ss := range s.getAllSessions() {
		sstats := ss.Stats()
		if sstats.Active {
			stats.NumSessions++
		}
		stats.NumStreams += sstats.NumStreams
		if prev, ok := sessionMap[sstats.Name]; !ok || sstats.LastChanged.After(prev.LastChanged) {
			sessionMap[sstats.Name] = sstats
		}
	}
	for _, sstats := range sessionMap {
		stats.sessions = append(stats.sessions, sstats)
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
	cfg, _ := s.getConfig()
	setTCPConnParams(cfg.TCP, dialed)
	return dialed, nil
}

func (s *Server) serveOne(accepted net.Conn, handler Handler) {
	defer func() {
		if r := recover(); r != nil {
			slog.Stackf(slog.LevelError, 0, "panic: %v", r)
		}
	}()
	ctx := s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer s.ctx.cancel(ctx)
	handler.Serve(ctx, accepted)
}

// Serve incoming connections with the given handler
func (s *Server) Serve(listener net.Listener, handler Handler) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, io.EOF) ||
				errors.Is(err, net.ErrClosed) {
				return
			}
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				slog.Warningf("serve: %s", formats.Error(err))
				time.Sleep(500 * time.Millisecond)
				continue
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

// serveSession handles one accepted mux session.
// It creates an inbound ephemeral tunnel, keeps it registered for lookup, and
// removes it when the underlying mux session closes.
func (s *Server) serveSession(ss *mux.Session) {
	// When the group closes, close ss to unblock Accept().
	if err := s.g.Go(func() {
		select {
		case <-s.g.CloseC():
			_ = ss.Close()
		case <-ss.CloseChan():
		}
	}); err != nil {
		return
	}
	now := time.Now()
	msg := fmt.Sprintf("%s: session established", ss.Tag())
	slog.Notice(msg)
	s.recentEvents.Add(now, msg)
	s.stats.authorized.Add(1)
	inbound := newTunnel(ss.PeerID(), "", s)
	inbound.ss = ss
	inbound.lastChanged = now
	s.addSession(inbound)
	s.numSessions.Add(1)
	defer func() {
		now := time.Now()
		msg := fmt.Sprintf("%s: session closed", ss.Tag())
		slog.Notice(msg)
		s.recentEvents.Add(now, msg)
		s.numSessions.Add(^uint32(0))
		s.removeSession(inbound)
	}()
	for {
		stream, err := ss.Accept()
		if err != nil {
			return
		}
		if err := s.g.Go(func() {
			s.handleInboundStream(ss.PeerID(), stream)
		}); err != nil {
			ioClose(stream)
			return
		}
	}
}

// handleInboundStream forwards one accepted server-side stream to the configured connect address.
func (s *Server) handleInboundStream(peerID string, stream net.Conn) {
	started := false
	defer func() {
		if !started {
			_ = stream.Close()
		}
	}()
	s.stats.request.Add(1)
	cfg, _ := s.getConfig()
	dialAddr := cfg.ServiceEntry(peerID).Connect
	if dialAddr == "" {
		dialAddr = cfg.Connect
	}
	if dialAddr == "" {
		peerDisplay := "?"
		if peerID != "" {
			peerDisplay = fmt.Sprintf("%q", peerID)
		}
		slog.Warningf("stream %s: no connect address configured", peerDisplay)
		return
	}
	tag := fmt.Sprintf("%q -> %s", peerID, dialAddr)
	ctx := s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer s.ctx.cancel(ctx)
	dialed, err := s.dialDirect(ctx, dialAddr)
	if err != nil {
		slog.Errorf("%s: %v", tag, err)
		return
	}
	if err := s.f.Start(stream, dialed, forwarder.HandlerFuncs{
		WriteClosed: func(conn net.Conn, err error) {
			if err != nil {
				slog.Debugf("%s: half-close %v: %s", tag, conn.RemoteAddr(), formats.Error(err))
			} else {
				slog.Debugf("%s: half-close %v", tag, conn.RemoteAddr())
			}
		},
		Closed: func() {
			slog.Debugf("%s: stream finished", tag)
			s.stats.success.Add(1)
		},
	}); err != nil {
		slog.Errorf("%s: forward: %v", tag, err)
		_ = dialed.Close()
		return
	}
	started = true
}

// Listen starts listening on the given address
func (s *Server) Listen(addr string) (net.Listener, error) {
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Errorf("listen %s: %s", addr, formats.Error(err))
		return listener, err
	}
	slog.Infof("listen: %v", listener.Addr())
	return listener, err
}

// loadSessions creates and stops config-driven tunnels to match cfg.
func (s *Server) loadSessions(cfg *config.File) error {
	// collect all peer names that should be active
	activePeers := make(map[string]struct{})
	for name := range cfg.Service.Peers {
		activePeers[name] = struct{}{}
	}
	for name := range cfg.Service.Listen {
		activePeers[name] = struct{}{}
	}
	// the empty-string key represents the default unnamed service driven by top-level Listen/MuxConnect
	if cfg.Listen != "" || cfg.MuxConnect != "" {
		activePeers[""] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 1. stop config-driven tunnels that are no longer in the config
	for name, ss := range s.services {
		if _, ok := activePeers[name]; !ok {
			if err := ss.Stop(); err != nil {
				slog.Errorf("session %q: %s", name, formats.Error(err))
			}
			for i, t := range s.sessions {
				if t == ss {
					s.sessions = append(s.sessions[:i], s.sessions[i+1:]...)
					break
				}
			}
			delete(s.services, name)
		}
	}
	// 2. start config-driven tunnels for newly active peers
	for name := range activePeers {
		if _, exists := s.services[name]; exists {
			continue
		}
		dialAddr := cfg.ServiceEntry(name).MuxConnect
		ss := newTunnel(name, dialAddr, s)
		s.services[name] = ss
		s.sessions = append(s.sessions, ss)
		if err := ss.Start(); err != nil {
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
		startupStart, startupRate, startupFull := c.ParsedMaxStartups()
		s.l = hlistener.Wrap(l, &hlistener.Config{
			Start:       uint32(startupStart),
			Full:        uint32(startupFull),
			Rate:        float64(startupRate) / 100.0,
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
	if s.cfg.APIListen != "" {
		l, err := s.Listen(s.cfg.APIListen)
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
	if err := s.loadSessions(s.cfg); err != nil {
		return err
	}
	s.started = time.Now()
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	// stop all listeners
	if s.l != nil {
		ioClose(s.l)
		s.l = nil
	}
	if s.apiListener != nil {
		ioClose(s.apiListener)
		s.apiListener = nil
	}
	// stop all config-driven tunnels so their per-service listeners exit Accept()
	s.mu.RLock()
	services := make([]*tunnel, 0, len(s.services))
	for _, ss := range s.services {
		services = append(services, ss)
	}
	s.mu.RUnlock()
	for _, ss := range services {
		if err := ss.Stop(); err != nil {
			slog.Errorf("session %q: %s", ss.id, formats.Error(err))
		}
	}
	// cancel all contexts
	s.ctx.close()
	// signal all goroutines to stop
	s.g.Close()
	// close all active HTTP/2 sessions to unblock Serve loops
	for _, ss := range s.getAllSessions() {
		if ss := ss.getSession(); ss != nil {
			_ = ss.Close()
		}
	}
	// close all forwards
	s.f.Close()
	slog.Info("waiting for unfinished connections")
	s.g.Wait()
	return nil
}

// LoadConfig reloads the configuration file
func (s *Server) LoadConfig(cfg *config.File) error {
	tlscfg, err := cfg.NewTLSConfig(appFlags.ServerName)
	if err != nil {
		return err
	}
	if err := s.loadSessions(cfg); err != nil {
		return err
	}
	func() {
		s.cfgMu.Lock()
		defer s.cfgMu.Unlock()
		s.cfg = cfg
		s.tlscfg = tlscfg
	}()
	s.recentEvents.Add(time.Now(), "config loaded")
	slog.Notice("config loaded")
	return nil
}

func (s *Server) getConfig() (*config.File, *tls.Config) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg, s.tlscfg
}
