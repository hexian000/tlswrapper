// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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
	"github.com/hexian000/tlswrapper/v4/mux/h2mux"
	"github.com/hexian000/tlswrapper/v4/mux/h3mux"
)

const network = "tcp"

var (
	ErrNoDialAddress  = errors.New("no dial address is configured")
	ErrDialInProgress = errors.New("another dial is in progress")
	ErrNoSession      = errors.New("no active session")
	ErrTunnelStopped  = errors.New("tunnel is stopped")
)

// Server owns listeners, config-driven tunnels, and active mux sessions.
type Server struct {
	cfg    *config.File
	tlscfg *tls.Config
	cfgMu  sync.RWMutex

	// listenMu guards l, muxListener, and apiListener, which are swapped by
	// config reloads while Stats() reads them from API handler goroutines.
	listenMu    sync.Mutex
	l           acceptStats  // accept counters of the active mux listener
	muxListener mux.Listener // active mux listener (h2mux or h3mux)
	apiListener net.Listener

	f forwarder.Forwarder

	recentEvents eventlog.Recent

	mu              sync.RWMutex
	mainTunnel      *tunnel                      // top-level cfg.MuxConnect tunnel
	localListener   net.Listener                 // top-level cfg.Listen listener
	localListenAddr string                       // address currently bound by localListener
	identityTunnels []*tunnel                    // cfg.Identity.MuxConnect[i] tunnels (positional)
	identities      map[string]*identityListener // cfg.Identity.Listen[name] listeners
	acceptedTunnels map[mux.Session]*tunnel      // inbound tunnels keyed by their mux session
	ctx             contextMgr

	dialer    net.Dialer
	muxDialer mux.Dialer
	g         routines.Group

	reloadMu       sync.Mutex
	lastConfigJSON []byte

	started time.Time

	stats struct {
		numSessions          atomic.Uint32
		numSessionsCreated   atomic.Uint64
		numSessionsFinalized atomic.Uint64
		numHalfOpen          atomic.Uint32
		accepted             atomic.Uint64 // mux sessions accepted (after handshake)
		served               atomic.Uint64 // mux sessions handed to serveSession
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
		identityTunnels: make([]*tunnel, 0),
		identities:      make(map[string]*identityListener),
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
	tlscfg, err := cfg.NewTLSConfig()
	if err != nil {
		return nil, err
	}
	s.tlscfg = tlscfg
	s.muxDialer = s.buildMuxDialer(cfg, tlscfg)
	return s, nil
}

func maxStreams(cfg *config.File) int {
	if cfg.Mux.MaxStreams == 0 {
		return 1024
	}
	return cfg.Mux.MaxStreams
}

// findSession returns the outbound tunnel whose active session has the given
// peer identity. An empty peerIdentity returns the top-level mainTunnel.
// When no session is active, it falls back to the tunnel that last reached
// this identity so OpenStream can dial on demand.
func (s *Server) findSession(peerIdentity string) *tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if peerIdentity == "" {
		return s.mainTunnel
	}
	var fallback *tunnel
	for _, t := range s.identityTunnels {
		if sess := t.getSession(); sess != nil && sess.PeerIdentity() == peerIdentity {
			return t
		}
		if fallback == nil && t.lastIdentityValue() == peerIdentity {
			fallback = t
		}
	}
	return fallback
}

func (s *Server) getAllTunnels() []*tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cap := len(s.identityTunnels) + len(s.acceptedTunnels)
	if s.mainTunnel != nil {
		cap++
	}
	result := make([]*tunnel, 0, cap)
	if s.mainTunnel != nil {
		result = append(result, s.mainTunnel)
	}
	result = append(result, s.identityTunnels...)
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
func (s *Server) flushSessionMetrics(ss mux.Session) {
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
	s.listenMu.Lock()
	l := s.l
	s.listenMu.Unlock()
	if l != nil {
		stats.Accepted, stats.Served = l.Stats()
	} else {
		// No mux listener active; read from unified atomic counters.
		stats.Accepted = s.stats.accepted.Load()
		stats.Served = s.stats.served.Load()
	}
	allTunnels := s.getAllTunnels()
	sessionMap := make(map[string]SessionStats)
	for _, t := range allTunnels {
		v := t.Stats()
		if v.Active {
			stats.NumSessions++
		}
		// All tunnels contribute to aggregated stream-open and traffic counts,
		// regardless of whether they have a peer identity.
		stats.StreamOpenActive += v.StreamsOpened
		stats.StreamOpenPassive += v.StreamsAccepted
		stats.BytesReceived += v.BytesReceived
		stats.BytesSent += v.BytesSent
		stats.WireLengthReceived += v.WireLengthReceived
		stats.WireLengthSent += v.WireLengthSent
		// Only sessions with a known peer identity are listed individually.
		if v.PeerIdentity != "" {
			if prev, ok := sessionMap[v.PeerIdentity]; !ok || v.LastChanged.After(prev.LastChanged) {
				sessionMap[v.PeerIdentity] = v
			}
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
	// Add cumulative bytes from already-closed sessions.
	stats.BytesReceived += s.stats.payloadBytesReceived.Load()
	stats.BytesSent += s.stats.payloadBytesSent.Load()
	stats.WireLengthReceived += s.stats.muxBytesReceived.Load()
	stats.WireLengthSent += s.stats.muxBytesSent.Load()
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

// getMuxDialer returns the current mux.Dialer, safe for concurrent use.
func (s *Server) getMuxDialer() mux.Dialer {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.muxDialer
}

// buildH3MuxDialer constructs a new H3Mux dialer from cfg and tlscfg.
func (s *Server) buildH3MuxDialer(cfg *config.File, tlscfg *tls.Config) *h3mux.H3Mux {
	return h3mux.New(&h3mux.Config{
		TLSConfig:                      tlscfg,
		LocalID:                        cfg.Identity.Claim,
		RejectInbound:                  cfg.Connect == "",
		KeepAlivePeriod:                cfg.KeepAlive(),
		HandshakeTimeout:               cfg.ConnectTimeout(),
		MaxIdleTimeout:                 cfg.IdleTimeout(),
		MaxIncomingStreams:             int64(cfg.Mux.MaxHalfOpen),
		InitialConnectionReceiveWindow: uint64(cfg.Mux.SessionWindow),
		MaxConnectionReceiveWindow:     64 << 20, // auto-tuning cap; quic-go default is 15 MiB
		InitialStreamReceiveWindow:     uint64(cfg.Mux.StreamWindow),
		MaxStreamReceiveWindow:         16 << 20, // auto-tuning cap; quic-go default is 6 MiB
	})
}

// buildMuxDialer constructs the appropriate mux.Dialer for cfg.MuxProtocol.
func (s *Server) buildMuxDialer(cfg *config.File, tlscfg *tls.Config) mux.Dialer {
	if cfg.MuxProtocol == "h3mux" {
		return s.buildH3MuxDialer(cfg, tlscfg)
	}
	return s.buildH2MuxDialer(cfg, tlscfg)
}

// startMuxListen creates and starts a mux listener for addr based on
// cfg.MuxProtocol. It sets s.l (h2mux only) and s.muxListener, then
// launches the accept goroutine.
func (s *Server) startMuxListen(cfg *config.File, addr string) error {
	var ml mux.Listener
	var lstats acceptStats
	startupStart, startupRate, startupFull := cfg.ParsedMaxStartups()
	if cfg.MuxProtocol == "h3mux" {
		// TLSConfigProvider fetches the current TLS config per connection so
		// that certificate rotation takes effect without restarting the listener.
		l, err := h3mux.ListenMux(addr, &h3mux.Config{
			TLSConfigProvider:              func() *tls.Config { _, tlscfg := s.getConfig(); return tlscfg },
			LocalID:                        cfg.Identity.Claim,
			RejectInbound:                  cfg.Connect == "",
			KeepAlivePeriod:                cfg.KeepAlive(),
			HandshakeTimeout:               cfg.ConnectTimeout(),
			MaxIdleTimeout:                 cfg.IdleTimeout(),
			MaxIncomingStreams:             int64(cfg.Mux.MaxHalfOpen),
			InitialConnectionReceiveWindow: uint64(cfg.Mux.SessionWindow),
			MaxConnectionReceiveWindow:     64 << 20, // auto-tuning cap; quic-go default is 15 MiB
			InitialStreamReceiveWindow:     uint64(cfg.Mux.StreamWindow),
			MaxStreamReceiveWindow:         16 << 20, // auto-tuning cap; quic-go default is 6 MiB
		})
		if err != nil {
			return err
		}
		// QUIC has no TCP-level accept hook, so session/startup throttling is
		// applied at the mux session level instead of via hlistener.
		hml := newHardenedMuxListener(l, hardenedMuxListenerConfig{
			Start:       uint32(startupStart),
			Full:        uint32(startupFull),
			Rate:        float64(startupRate) / 100.0,
			MaxSessions: uint32(cfg.MaxSessions),
			Stats:       s.ListenerStats,
		})
		ml = hml
		lstats = hml
	} else {
		l, err := s.Listen(addr)
		if err != nil {
			return err
		}
		hl := hlistener.Wrap(l, &hlistener.Config{
			Start:       uint32(startupStart),
			Full:        uint32(startupFull),
			Rate:        float64(startupRate) / 100.0,
			MaxSessions: uint32(cfg.MaxSessions),
			Stats:       s.ListenerStats,
		})
		ml = s.buildH2MuxListener(hl, cfg)
		lstats = hl
	}
	slog.Noticef("mux listen: %v", ml.Addr())
	if err := s.g.Go(func() { s.serveMuxListener(ml) }); err != nil {
		ioClose(ml)
		return err
	}
	s.listenMu.Lock()
	s.muxListener = ml
	s.l = lstats
	s.listenMu.Unlock()
	return nil
}

// buildH2MuxDialer constructs a new H2Mux dialer from cfg and tlscfg.
func (s *Server) buildH2MuxDialer(cfg *config.File, tlscfg *tls.Config) *h2mux.H2Mux {
	return h2mux.New(&h2mux.Config{
		TLSConfig:     tlscfg,
		LocalID:       cfg.Identity.Claim,
		RejectInbound: cfg.Connect == "",
		KeepAlive:     cfg.KeepAlive(),
		PingTimeout:   cfg.PingTimeout(),
		WriteTimeout:  cfg.SendTimeout(),
		SessionWindow: int32(cfg.Mux.SessionWindow),
		StreamWindow:  int32(cfg.Mux.StreamWindow),
		Dialer:        s.dialer,
		ConnSetup:     func(c net.Conn) { setTCPConnParams(cfg.Mux.TCP, c) },
	})
}

// buildH2MuxListener wraps l as a mux.Listener using the h2mux server-side
// handshake.  TLS config is fetched per-connection via TLSConfigProvider so
// that certificate rotation takes effect without restarting the listener.
func (s *Server) buildH2MuxListener(l net.Listener, cfg *config.File) *h2mux.H2Listener {
	return h2mux.NewListener(l, &h2mux.Config{
		TLSConfigProvider:    func() *tls.Config { _, tlscfg := s.getConfig(); return tlscfg },
		LocalID:              cfg.Identity.Claim,
		RejectInbound:        cfg.Connect == "",
		WriteTimeout:         cfg.SendTimeout(),
		SessionWindow:        int32(cfg.Mux.SessionWindow),
		StreamWindow:         int32(cfg.Mux.StreamWindow),
		MaxConcurrentStreams: uint32(cfg.Mux.MaxHalfOpen),
		IdleTimeout:          cfg.IdleTimeout(),
		ConnSetup: func(c net.Conn) {
			cur, _ := s.getConfig()
			setTCPConnParams(cur.Mux.TCP, c)
		},
	})
}

// serveMuxListener runs the accept loop for a mux.Listener.
// It respects hlistener rate-limiting counters and calls serveSession for
// each successfully accepted session.  The protocol handshake is run
// concurrently in a goroutine so that slow clients cannot block new accepts.
func (s *Server) serveMuxListener(l mux.Listener) {
	for {
		start := time.Now()
		ss, err := l.Accept()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				slog.Warningf("accept session: %s", formats.Error(err))
				time.Sleep(500 * time.Millisecond)
				continue
			}
			slog.Errorf("accept session: %s", formats.Error(err))
			return
		}
		s.stats.accepted.Add(1)
		if err := s.g.Go(func() {
			ctx := s.ctx.withTimeout()
			if ctx == nil {
				_ = ss.Close()
				return
			}
			s.stats.numHalfOpen.Add(1)
			err := ss.Handshake(ctx)
			s.ctx.cancel(ctx)
			s.stats.numHalfOpen.Add(^uint32(0))
			if err != nil {
				slog.Warningf("handshake: %s", formats.Error(err))
				return
			}
			s.stats.served.Add(1)
			s.serveSession(ss, time.Since(start))
		}); err != nil {
			_ = ss.Close()
			slog.Errorf("accept session: %s", formats.Error(err))
		}
	}
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
		s.flushSessionMetrics(ss)
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
	dialAddr := cfg.Connect
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

// loadTunnels reconciles identity listeners and outbound tunnels with cfg.
func (s *Server) loadTunnels(cfg *config.File) error {
	var errs []error

	s.mu.Lock()
	defer s.mu.Unlock()

	// === Part 0: Reconcile mainTunnel (top-level cfg.MuxConnect) ===
	if s.mainTunnel != nil && s.mainTunnel.dialAddr != cfg.MuxConnect {
		if err := s.mainTunnel.Stop(); err != nil {
			tag := s.mainTunnel.tagValue()
			slog.Errorf("%s: %s", tag, formats.Error(err))
			errs = append(errs, fmt.Errorf("stop main tunnel: %w", err))
		}
		s.mainTunnel = nil
	}
	if cfg.MuxConnect != "" && s.mainTunnel == nil {
		t := newTunnel(cfg.MuxConnect, s)
		t.tag = t.buildTunnelTag(nil, nil)
		s.mainTunnel = t
		if err := t.Start(); err != nil {
			tag := t.tagValue()
			slog.Errorf("%s: start: %s", tag, formats.Error(err))
			errs = append(errs, fmt.Errorf("start main tunnel: %w", err))
		}
	}

	// === Part 0b: Reconcile localListener (top-level cfg.Listen) ===
	if s.localListenAddr != cfg.Listen {
		if s.localListener != nil {
			ioClose(s.localListener)
			s.localListener = nil
			s.localListenAddr = ""
		}
		if cfg.Listen != "" {
			l, err := s.Listen(cfg.Listen)
			if err != nil {
				errs = append(errs, fmt.Errorf("listen %s: %w", cfg.Listen, err))
			} else {
				h := &LocalHandler{s: s, id: ""}
				if err := s.g.Go(func() { s.Serve(l, h) }); err != nil {
					slog.Errorf("local listen goroutine: %s", formats.Error(err))
					errs = append(errs, fmt.Errorf("local listen goroutine: %w", err))
					ioClose(l)
				} else {
					s.localListener = l
					s.localListenAddr = cfg.Listen
				}
			}
		}
	}

	// === Part 1: Reconcile identity listeners (cfg.Identity.Listen) ===
	activeListens := make(map[string]string)
	for name, addr := range cfg.Identity.Listen {
		activeListens[name] = addr
	}
	// Stop identity listeners that were removed or whose address changed.
	for id, il := range s.identities {
		if addr, ok := activeListens[id]; ok && addr == il.addr {
			delete(activeListens, id) // unchanged, retain
		} else {
			delete(s.identities, id)
			il.stop()
		}
	}
	// Create new identity listeners.
	for id, listenAddr := range activeListens {
		l, err := s.Listen(listenAddr)
		if err != nil {
			slog.Errorf("identity %q: listen: %s", id, formats.Error(err))
			errs = append(errs, fmt.Errorf("identity %q: listen: %w", id, err))
			continue
		}
		il := &identityListener{id: id, addr: listenAddr, l: l}
		if err := il.start(s); err != nil {
			slog.Errorf("identity %q: start: %s", id, formats.Error(err))
			errs = append(errs, fmt.Errorf("identity %q: start: %w", id, err))
			ioClose(l)
			continue
		}
		s.identities[id] = il
	}

	// === Part 2: Reconcile identityTunnels by position (cfg.Identity.MuxConnect) ===
	desired := cfg.Identity.MuxConnect
	// Find the first position where config diverges from the current slice.
	keepCount := min(len(s.identityTunnels), len(desired))
	for i := range keepCount {
		if s.identityTunnels[i].dialAddr != desired[i] {
			keepCount = i
			break
		}
	}
	// Stop tunnels past the keep boundary.
	for i := keepCount; i < len(s.identityTunnels); i++ {
		if err := s.identityTunnels[i].Stop(); err != nil {
			slog.Errorf("tunnel %s: %s", s.identityTunnels[i].dialAddr, formats.Error(err))
			errs = append(errs, fmt.Errorf("stop tunnel %s: %w", s.identityTunnels[i].dialAddr, err))
		}
	}
	s.identityTunnels = s.identityTunnels[:keepCount]
	// Create new tunnels for added entries.
	for i := keepCount; i < len(desired); i++ {
		t := newTunnel(desired[i], s)
		t.tag = t.buildTunnelTag(nil, nil)
		s.identityTunnels = append(s.identityTunnels, t)
		if err := t.Start(); err != nil {
			tag := t.tagValue()
			slog.Errorf("%s: start: %s", tag, formats.Error(err))
			errs = append(errs, fmt.Errorf("start %s: %w", tag, err))
		}
	}
	return errors.Join(errs...)
}

// reloadMuxListen restarts the MuxListen listener when its configuration
// changed from old to cfg. Errors are logged; the reload continues regardless.
// The mux listener captures its tunables (windows, timeouts, stream limits,
// identity claim, RejectInbound) at build time, so any change to them requires
// a restart; TLS material is excluded because it is fetched per connection.
func (s *Server) reloadMuxListen(old, cfg *config.File) error {
	if cfg.MuxListen == old.MuxListen &&
		cfg.MuxProtocol == old.MuxProtocol &&
		cfg.MaxSessions == old.MaxSessions &&
		cfg.MaxStartups == old.MaxStartups &&
		cfg.Mux == old.Mux &&
		cfg.Identity.Claim == old.Identity.Claim &&
		cfg.Connect == old.Connect {
		return nil
	}
	s.listenMu.Lock()
	ml := s.muxListener
	s.muxListener = nil
	s.l = nil
	s.listenMu.Unlock()
	if ml != nil {
		ioClose(ml)
	}
	if cfg.MuxListen == "" {
		return nil
	}
	if err := s.startMuxListen(cfg, cfg.MuxListen); err != nil {
		slog.Errorf("reload: mux listen %s: %s", cfg.MuxListen, formats.Error(err))
		return fmt.Errorf("reload mux listen: %w", err)
	}
	return nil
}

// reloadAPIListen restarts the APIListen HTTP server when its address changed
// from old to cfg. Errors are logged; the reload continues regardless.
func (s *Server) reloadAPIListen(old, cfg *config.File) error {
	if cfg.APIListen == old.APIListen {
		return nil
	}
	s.listenMu.Lock()
	al := s.apiListener
	s.apiListener = nil
	s.listenMu.Unlock()
	if al != nil {
		ioClose(al)
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
	s.listenMu.Lock()
	s.apiListener = l
	s.listenMu.Unlock()
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
			h2mux.DrainPool()
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
	// Set before any serving goroutine starts; read concurrently by API handlers.
	s.started = time.Now()
	if s.cfg.MuxListen != "" {
		c, _ := s.getConfig()
		if err := s.startMuxListen(c, s.cfg.MuxListen); err != nil {
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
		s.listenMu.Lock()
		s.apiListener = l
		s.listenMu.Unlock()
	}
	if err := s.loadTunnels(s.cfg); err != nil {
		return err
	}
	if err := s.g.Go(s.maintenanceLoop); err != nil {
		return err
	}
	return nil
}

// Shutdown stops listeners, tunnels, sessions, and forwarders in that order.
func (s *Server) Shutdown() error {
	s.listenMu.Lock()
	ml, al := s.muxListener, s.apiListener
	s.muxListener, s.l, s.apiListener = nil, nil, nil
	s.listenMu.Unlock()
	if ml != nil {
		ioClose(ml)
	}
	if al != nil {
		ioClose(al)
	}
	// Snapshot all listeners and tunnels before stopping.
	s.mu.RLock()
	localL := s.localListener
	listeners := make([]*identityListener, 0, len(s.identities))
	for _, il := range s.identities {
		listeners = append(listeners, il)
	}
	main := s.mainTunnel
	identity := make([]*tunnel, len(s.identityTunnels))
	copy(identity, s.identityTunnels)
	s.mu.RUnlock()
	// Close local listener so its Accept loop exits.
	if localL != nil {
		ioClose(localL)
	}
	// Stop identity listeners so their Accept loops exit.
	for _, il := range listeners {
		il.stop()
	}
	// Stop config-driven tunnels.
	if main != nil {
		if err := main.Stop(); err != nil {
			tag := main.tagValue()
			slog.Errorf("%s: %s", tag, formats.Error(err))
		}
	}
	for _, t := range identity {
		if err := t.Stop(); err != nil {
			tag := t.tagValue()
			slog.Errorf("%s: %s", tag, formats.Error(err))
		}
	}
	s.ctx.close()
	// Close sessions before closing the group: this lets Accept loops return
	// naturally so serveSession goroutines can finish their own cleanup before
	// the group signals them to stop.
	for _, ss := range s.getAllTunnels() {
		if ss := ss.getSession(); ss != nil {
			_ = ss.Close()
		}
	}
	s.g.Close()
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
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	// Skip reload when config is identical to the last successfully applied
	// snapshot; remember it only on success so a failed reload can be retried
	// with the same config.
	var cfgJSON []byte
	if b, err := json.Marshal(cfg); err == nil {
		if bytes.Equal(s.lastConfigJSON, b) {
			return nil
		}
		cfgJSON = b
	}
	var errs []error
	// 1. Build new TLS config; retain old one on failure.
	newTLSCfg, err := cfg.NewTLSConfig()
	if err != nil {
		slog.Errorf("reload: TLS config: %s", formats.Error(err))
		newTLSCfg = nil
		errs = append(errs, fmt.Errorf("reload TLS config: %w", err))
	}
	// 2. Swap the config snapshot first so that listeners or sessions
	// (re)created by the following steps observe the new config and TLS
	// material rather than the old ones.
	s.cfgMu.Lock()
	old := s.cfg
	s.cfg = cfg
	if newTLSCfg != nil {
		s.tlscfg = newTLSCfg
	}
	s.muxDialer = s.buildMuxDialer(cfg, s.tlscfg)
	s.cfgMu.Unlock()
	s.f.SetLimit(maxStreams(cfg))
	// 3. Mark all existing sessions as stale so they age out when idle.
	s.markSessionsStale()
	// 4. Reload listeners when addresses/limits change.
	if err := s.reloadMuxListen(old, cfg); err != nil {
		errs = append(errs, err)
	}
	if err := s.reloadAPIListen(old, cfg); err != nil {
		errs = append(errs, err)
	}
	// 5. Sync config-driven tunnels.
	if err := s.loadTunnels(cfg); err != nil {
		errs = append(errs, err)
	}
	s.recentEvents.Add(time.Now(), "config loaded")
	slog.Notice("config loaded")
	if err := errors.Join(errs...); err != nil {
		return err
	}
	s.lastConfigJSON = cfgJSON
	return nil
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
