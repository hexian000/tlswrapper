// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexian000/gosnippets/formats"
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

// Server owns listeners, config-driven tunnels, and active mux sessions.
type Server struct {
	cfg    *config.File
	tlscfg *tls.Config
	cfgMu  sync.RWMutex

	l           hlistener.Listener
	apiListener net.Listener
	f           forwarder.Forwarder

	recentEvents eventlog.Recent

	mu              sync.RWMutex
	identityTunnels map[string]*tunnel      // config-driven tunnels, keyed by t.id
	acceptedTunnels map[mux.Session]*tunnel // inbound tunnels keyed by their mux session
	ctx             contextMgr

	dialer net.Dialer
	g      routines.Group

	started time.Time

	stats struct {
		numSessions          atomic.Uint32
		numSessionsCreated   atomic.Uint64
		numSessionsFinalized atomic.Uint64
		numHalfOpen          atomic.Uint32
		authorized           atomic.Uint64
		request              atomic.Uint64
		success              atomic.Uint64
		muxBytesReceived     atomic.Uint64 // cumulative mux wire bytes received from closed sessions
		muxBytesSent         atomic.Uint64 // cumulative mux wire bytes sent from closed sessions
		payloadBytesReceived atomic.Uint64 // cumulative gRPC Stream payload bytes received (TCP Traffic) from closed sessions
		payloadBytesSent     atomic.Uint64 // cumulative gRPC Stream payload bytes sent (TCP Traffic) from closed sessions
	}
}

// NewServer builds a Server from the initial config snapshot.
func NewServer(cfg *config.File) (*Server, error) {
	g := routines.NewGroup()
	s := &Server{
		cfg:             cfg,
		identityTunnels: make(map[string]*tunnel),
		acceptedTunnels: make(map[mux.Session]*tunnel),
		ctx: contextMgr{
			contexts: make(map[context.Context]context.CancelFunc),
		},
		f:            forwarder.New(maxStreams(cfg), g),
		recentEvents: eventlog.NewRecent(100),
		g:            g,
	}
	s.ctx.timeout = func() time.Duration {
		cfg, _ := s.getConfig()
		return cfg.ConnectTimeout()
	}
	tlscfg, err := cfg.NewTLSConfig(appFlags.ServerName)
	if err != nil {
		return nil, err
	}
	s.tlscfg = tlscfg
	return s, nil
}

func maxStreams(cfg *config.File) int {
	if cfg.Mux.MaxStreams == 0 {
		return 1024
	}
	return cfg.Mux.MaxStreams
}

// findSession matches either a tracked tunnel key or the remote identity
// learned from the handshake.
func (s *Server) findSession(peerIdentity string) *tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.identityTunnels[peerIdentity]; ok {
		if t.getSession() != nil {
			return t
		}
		// key matched but no active session; still search other tunnels by handshake identity
	}
	for id, t := range s.identityTunnels {
		if id == peerIdentity {
			continue
		}
		if sess := t.getSession(); sess != nil && sess.PeerIdentity() == peerIdentity {
			return t
		}
	}
	return nil
}

func (s *Server) getAllTunnels() []*tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*tunnel, 0, len(s.identityTunnels)+len(s.acceptedTunnels))
	for _, t := range s.identityTunnels {
		result = append(result, t)
	}
	for _, t := range s.acceptedTunnels {
		result = append(result, t)
	}
	return result
}

// markSessionsStale marks all currently tracked sessions as stale and
// immediately evicts those that are already idle (no active streams).
// Sessions with active streams are evicted by watchIdleSession when they
// become idle.
func (s *Server) markSessionsStale() {
	for _, t := range s.getAllTunnels() {
		t.mu.Lock()
		t.stale = true
		t.mu.Unlock()
		t.checkIdle()
	}
}

// flushSessionMetrics accumulates a closing session's gRPC byte counters into
// the server-level totals so Mux/TCP Traffic survive session reconnects.
func (s *Server) flushSessionMetrics(t *tunnel, ss mux.Session) {
	if m := ss.Stats(); m != nil {
		s.stats.payloadBytesReceived.Add(m.BytesReceived.Load())
		s.stats.payloadBytesSent.Add(m.BytesSent.Load())
		s.stats.muxBytesReceived.Add(m.WireLengthReceived.Load())
		s.stats.muxBytesSent.Add(m.WireLengthSent.Load())
	}
}

// StreamLatencyStats holds P50/P90/P99/MAX percentiles for stream open latency.
// Available is false when no samples have been recorded yet.
type StreamLatencyStats struct {
	P50, P90, P99, Max time.Duration
	Available          bool
}

// ServerStats is the snapshot returned by Server.Stats.
type ServerStats struct {
	NumSessions                        uint32
	NumSessionsCreated                 uint64
	NumSessionsFinalized               uint64
	NumHalfOpen                        uint32
	NumStreamsHalfOpen                 uint32
	StreamOpenActive                   uint64
	StreamOpenPassive                  uint64
	StreamLatency                      StreamLatencyStats
	BytesReceived, BytesSent           uint64
	WireLengthReceived, WireLengthSent uint64
	Accepted                           uint64
	Served                             uint64
	Authorized                         uint64
	ReqTotal                           uint64
	ReqSuccess                         uint64
	sessions                           []SessionStats
}

// Stats snapshots listener, traffic, and per-session metrics.
func (s *Server) Stats() (stats ServerStats) {
	if s.l != nil {
		stats.Accepted, stats.Served = s.l.Stats()
	}
	allTunnels := s.getAllTunnels()
	sessionMap := make(map[string]SessionStats)
	for _, t := range allTunnels {
		v := t.Stats()
		if v.Active {
			stats.NumSessions++
		}
		if prev, ok := sessionMap[v.Name]; !ok || v.LastChanged.After(prev.LastChanged) {
			sessionMap[v.Name] = v
		}
	}
	for _, sstats := range sessionMap {
		stats.sessions = append(stats.sessions, sstats)
	}
	stats.Authorized = s.stats.authorized.Load()
	stats.ReqTotal, stats.ReqSuccess = s.stats.request.Load(), s.stats.success.Load()
	stats.NumHalfOpen = s.stats.numHalfOpen.Load()
	stats.NumSessionsCreated = s.stats.numSessionsCreated.Load()
	stats.NumSessionsFinalized = s.stats.numSessionsFinalized.Load()
	stats.NumStreamsHalfOpen = uint32(s.f.HalfOpenCount())
	for _, ss := range stats.sessions {
		stats.StreamOpenActive += ss.StreamsOpened
		stats.StreamOpenPassive += ss.StreamsAccepted
	}
	var latSamples []time.Duration
	for _, t := range allTunnels {
		snap := t.streamLatency.Snapshot()
		for _, d := range snap {
			if d > 0 {
				latSamples = append(latSamples, d)
			}
		}
	}
	if len(latSamples) > 0 {
		slices.Sort(latSamples)
		n := len(latSamples)
		at := func(pct float64) time.Duration {
			i := int(math.Floor(float64(n) * pct))
			if i >= n {
				i = n - 1
			}
			return latSamples[i]
		}
		stats.StreamLatency = StreamLatencyStats{
			P50: at(0.50), P90: at(0.90), P99: at(0.99),
			Max: latSamples[n-1], Available: true,
		}
	}
	stats.BytesReceived = s.stats.payloadBytesReceived.Load()
	stats.BytesSent = s.stats.payloadBytesSent.Load()
	stats.WireLengthReceived = s.stats.muxBytesReceived.Load()
	stats.WireLengthSent = s.stats.muxBytesSent.Load()
	for _, ss := range stats.sessions {
		stats.BytesReceived += ss.BytesReceived
		stats.BytesSent += ss.BytesSent
		stats.WireLengthReceived += ss.WireLengthReceived
		stats.WireLengthSent += ss.WireLengthSent
	}
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

// Serve accepts until listener closes or Accept returns a fatal error.
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
func (s *Server) serveSession(ss mux.Session, setupDur time.Duration) {
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
	inbound := newTunnel("", s)
	inbound.mu.Lock()
	inbound.id = ss.PeerIdentity()
	inbound.ss = ss
	inbound.updateTagLocked(ss, nil)
	tag := inbound.tag
	inbound.lastChanged = now
	inbound.mu.Unlock()
	msg := fmt.Sprintf("%s: session established (setup: %s)", tag, formats.Duration(setupDur))
	slog.Notice(msg)
	s.recentEvents.Add(now, msg)
	s.stats.authorized.Add(1)
	s.mu.Lock()
	s.acceptedTunnels[ss] = inbound
	s.mu.Unlock()
	s.stats.numSessions.Add(1)
	s.stats.numSessionsCreated.Add(1)
	_ = s.g.Go(func() { inbound.watchIdleSession(ss) })
	defer func() {
		now := time.Now()
		msg := fmt.Sprintf("%s: session closed", tag)
		slog.Notice(msg)
		s.recentEvents.Add(now, msg)
		s.flushSessionMetrics(inbound, ss)
		s.stats.numSessions.Add(^uint32(0))
		s.stats.numSessionsFinalized.Add(1)
		s.mu.Lock()
		delete(s.acceptedTunnels, ss)
		s.mu.Unlock()
	}()
	for {
		stream, err := ss.Accept()
		if err != nil {
			return
		}
		if err := s.g.Go(func() {
			s.handleInboundStream(inbound, ss.PeerIdentity(), stream)
		}); err != nil {
			ioClose(stream)
			return
		}
	}
}

// acceptInboundStreams drains server-initiated streams from a client-side session.
func (s *Server) acceptInboundStreams(tn *tunnel, ss mux.Session) {
	for {
		stream, err := ss.Accept()
		if err != nil {
			return
		}
		peerIdentity := ss.PeerIdentity()
		if err := s.g.Go(func() {
			s.handleInboundStream(tn, peerIdentity, stream)
		}); err != nil {
			ioClose(stream)
			return
		}
	}
}

// handleInboundStream forwards one accepted server-side stream to the configured connect address.
func (s *Server) handleInboundStream(t *tunnel, peerIdentity string, stream net.Conn) {
	started := false
	defer func() {
		if !started {
			_ = stream.Close()
		}
	}()
	s.stats.request.Add(1)
	cfg, _ := s.getConfig()
	peerIdentityForTag := peerIdentity
	if t != nil {
		if peerIdentityForTag == "" {
			peerIdentityForTag = t.dialAddr
		}
	}
	tag := formatStreamTag(false, cfg.Identity.Claim, peerIdentity, peerIdentityForTag, stream.LocalAddr(), stream.RemoteAddr(), stream)
	dialAddr := cfg.ServiceEntry(peerIdentity).Connect
	if dialAddr == "" {
		dialAddr = cfg.Connect
	}
	if dialAddr == "" {
		slog.Warningf("%s: no connect address configured", tag)
		return
	}
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

// Listen binds a TCP listener and logs the bound address.
func (s *Server) Listen(addr string) (net.Listener, error) {
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Errorf("listen %s: %s", addr, formats.Error(err))
		return listener, err
	}
	slog.Infof("listen: %v", listener.Addr())
	return listener, err
}

// loadSessions reconciles config-driven tunnels with cfg.
func (s *Server) loadSessions(cfg *config.File) error {
	var errs []error
	// Keys come from Identity.Listen names, Identity.MuxConnect addresses, and
	// the default "" tunnel driven by the top-level Listen/MuxConnect fields.
	activePeers := make(map[string]struct{})
	for name := range cfg.Identity.Listen {
		activePeers[name] = struct{}{}
	}
	for _, addr := range cfg.Identity.MuxConnect {
		activePeers[addr] = struct{}{}
	}
	if cfg.Listen != "" || cfg.MuxConnect != "" {
		activePeers[""] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.identityTunnels {
		if _, ok := activePeers[id]; ok {
			delete(activePeers, id)
		} else {
			delete(s.identityTunnels, id)
			if err := t.Stop(); err != nil {
				slog.Errorf("session %q: %s", id, formats.Error(err))
				errs = append(errs, fmt.Errorf("stop session %q: %w", id, err))
			}
		}
	}
	for name := range activePeers {
		// Listen-only entries have no dial target; outbound-only entries use the
		// key itself because Identity.MuxConnect is keyed by address.
		var dialAddr string
		if name == "" {
			dialAddr = cfg.MuxConnect
		} else if _, isListen := cfg.Identity.Listen[name]; !isListen {
			dialAddr = name
		}
		ss := newTunnel(dialAddr, s)
		ss.id = name
		ss.tag = ss.buildTunnelTag(nil, nil)
		s.identityTunnels[name] = ss
		if err := ss.Start(cfg); err != nil {
			tag := ss.tagValue()
			slog.Errorf("%s: start: %s", tag, formats.Error(err))
			errs = append(errs, fmt.Errorf("start %s: %w", tag, err))
		}
	}
	return errors.Join(errs...)
}

// reloadMuxListen restarts the MuxListen listener when its configuration changes.
// Errors are logged; the reload continues regardless.
func (s *Server) reloadMuxListen(cfg *config.File) error {
	old, _ := s.getConfig()
	if cfg.MuxListen == old.MuxListen &&
		cfg.MaxSessions == old.MaxSessions &&
		cfg.MaxStartups == old.MaxStartups {
		return nil
	}
	if s.l != nil {
		ioClose(s.l)
		s.l = nil
	}
	if cfg.MuxListen == "" {
		return nil
	}
	l, err := s.Listen(cfg.MuxListen)
	if err != nil {
		slog.Errorf("reload: mux listen %s: %s", cfg.MuxListen, formats.Error(err))
		return fmt.Errorf("reload mux listen %s: %w", cfg.MuxListen, err)
	}
	slog.Noticef("mux listen: %v", l.Addr())
	h := &MuxHandler{s: s}
	startupStart, startupRate, startupFull := cfg.ParsedMaxStartups()
	s.l = hlistener.Wrap(l, &hlistener.Config{
		Start:       uint32(startupStart),
		Full:        uint32(startupFull),
		Rate:        float64(startupRate) / 100.0,
		MaxSessions: uint32(cfg.MaxSessions),
		Stats:       s.ListenerStats,
	})
	if err := s.g.Go(func() {
		s.Serve(s.l, h)
	}); err != nil {
		slog.Errorf("reload: mux listen goroutine: %s", formats.Error(err))
		ioClose(s.l)
		s.l = nil
		return fmt.Errorf("reload mux listen goroutine: %w", err)
	}
	return nil
}

// reloadAPIListen restarts the APIListen HTTP server when its address changes.
// Errors are logged; the reload continues regardless.
func (s *Server) reloadAPIListen(cfg *config.File) error {
	old, _ := s.getConfig()
	if cfg.APIListen == old.APIListen {
		return nil
	}
	if s.apiListener != nil {
		ioClose(s.apiListener)
		s.apiListener = nil
	}
	if cfg.APIListen == "" {
		return nil
	}
	l, err := s.Listen(cfg.APIListen)
	if err != nil {
		slog.Errorf("reload: api listen %s: %s", cfg.APIListen, formats.Error(err))
		return fmt.Errorf("reload api listen %s: %w", cfg.APIListen, err)
	}
	slog.Noticef("http listen: %v", l.Addr())
	if err := s.g.Go(func() {
		if err := RunHTTPServer(l, s); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Error(formats.Error(err))
		}
	}); err != nil {
		slog.Errorf("reload: api listen goroutine: %s", formats.Error(err))
		ioClose(l)
		return fmt.Errorf("reload api listen goroutine: %w", err)
	}
	s.apiListener = l
	return nil
}

// timeoutAllSessions closes every active session without stopping the tunnels.
// Config-driven tunnels will automatically redial; accepted tunnels are cleaned
// up by their serveSession defer.
func (s *Server) timeoutAllSessions() {
	for _, t := range s.getAllTunnels() {
		if ss := t.getSession(); ss != nil {
			_ = ss.Close()
		}
	}
}

// maintenanceLoop runs server-level housekeeping on a 10-second ticker:
//   - Drains one object from each sync.Pool to slowly return memory under low load.
//   - Detects device sleep (wall-clock advancing far beyond the expected interval,
//     as can happen on Android) and force-closes all sessions so they re-handshake
//     with fresh state.
func (s *Server) maintenanceLoop() {
	const interval = 10 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastTick := time.Now()
	for {
		select {
		case now := <-ticker.C:
			elapsed := now.Sub(lastTick)
			lastTick = now
			// Slowly release pooled objects so the GC can reclaim idle memory.
			mux.DrainPool()
			forwarder.DrainPool()
			// If the wall clock advanced more than the ping timeout the device
			// almost certainly slept (common on Android). The peer would already
			// have declared the session dead via keepalive, so force-close all
			// sessions to trigger a clean re-handshake.
			cfg, _ := s.getConfig()
			if elapsed > cfg.PingTimeout() {
				slog.Warningf("system sleep detected (elapsed: %v), closing all sessions", elapsed)
				s.timeoutAllSessions()
			}
		case <-s.g.CloseC():
			return
		}
	}
}

// Start brings up listeners, maintenance, and config-driven tunnels.
func (s *Server) Start() error {
	if s.cfg.MuxListen != "" {
		l, err := s.Listen(s.cfg.MuxListen)
		if err != nil {
			return err
		}
		slog.Noticef("mux listen: %v", l.Addr())
		h := &MuxHandler{s: s}
		c, _ := s.getConfig()
		startupStart, startupRate, startupFull := c.ParsedMaxStartups()
		s.l = hlistener.Wrap(l, &hlistener.Config{
			Start:       uint32(startupStart),
			Full:        uint32(startupFull),
			Rate:        float64(startupRate) / 100.0,
			MaxSessions: uint32(c.MaxSessions),
			Stats:       s.ListenerStats,
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
	if err := s.g.Go(s.maintenanceLoop); err != nil {
		return err
	}
	s.started = time.Now()
	return nil
}

// Shutdown stops listeners, tunnels, sessions, and forwarders in that order.
func (s *Server) Shutdown() error {
	if s.l != nil {
		ioClose(s.l)
		s.l = nil
	}
	if s.apiListener != nil {
		ioClose(s.apiListener)
		s.apiListener = nil
	}
	// Stop config-driven tunnels first so their local listeners exit Accept().
	s.mu.RLock()
	identity := make([]*tunnel, 0, len(s.identityTunnels))
	for _, t := range s.identityTunnels {
		identity = append(identity, t)
	}
	s.mu.RUnlock()
	for _, t := range identity {
		if err := t.Stop(); err != nil {
			tag := t.tagValue()
			slog.Errorf("%s: %s", tag, formats.Error(err))
		}
	}
	s.ctx.close()
	s.g.Close()
	// Closing sessions unblocks any remaining Accept loops.
	for _, ss := range s.getAllTunnels() {
		if ss := ss.getSession(); ss != nil {
			_ = ss.Close()
		}
	}
	s.f.Close()
	slog.Info("waiting for unfinished connections")
	waitDone := make(chan struct{})
	go func() { s.g.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		slog.Warning("graceful shutdown timed out, forcing exit")
	}
	return nil
}

// ReloadConfig reloads the configuration file.
// All sub-steps are attempted regardless of individual failures; errors are
// logged but do not abort the reload. Any failures are joined in the returned
// error after the best-effort reload completes.
func (s *Server) ReloadConfig(cfg *config.File) error {
	var errs []error
	// 1. Build new TLS config; retain old one on failure.
	newTLSCfg, err := cfg.NewTLSConfig(appFlags.ServerName)
	if err != nil {
		slog.Errorf("reload: TLS config: %s", formats.Error(err))
		newTLSCfg = nil
		errs = append(errs, fmt.Errorf("reload TLS config: %w", err))
	}
	// 2. Mark all existing sessions as stale so they age out when idle.
	s.markSessionsStale()
	// 3. Reload listeners when addresses/limits change.
	if err := s.reloadMuxListen(cfg); err != nil {
		errs = append(errs, err)
	}
	if err := s.reloadAPIListen(cfg); err != nil {
		errs = append(errs, err)
	}
	// 4. Sync config-driven tunnels.
	if err := s.loadSessions(cfg); err != nil {
		errs = append(errs, err)
	}
	// 5. Atomically swap config; retain old TLS config if reload failed.
	s.cfgMu.Lock()
	s.cfg = cfg
	if newTLSCfg != nil {
		s.tlscfg = newTLSCfg
	}
	s.cfgMu.Unlock()
	s.recentEvents.Add(time.Now(), "config loaded")
	slog.Notice("config loaded")
	return errors.Join(errs...)
}

// ListenerStats reports active sessions and mux handshakes still in progress.
func (s *Server) ListenerStats() (uint32, uint32) {
	return s.stats.numSessions.Load(), s.stats.numHalfOpen.Load()
}

func (s *Server) getConfig() (*config.File, *tls.Config) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg, s.tlscfg
}
