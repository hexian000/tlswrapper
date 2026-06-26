// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/mux"
)

// tunnel owns at most one active mux session.
// Config-driven outbound tunnels are tracked in Server.identityTunnels and own
// a redial loop. Inbound tunnels are created for accepted sessions and
// disappear with that session.
type tunnel struct {
	dialAddr string // outbound dial target; empty for inbound accepted sessions
	s        *Server

	mu            sync.RWMutex
	tag           string
	ss            mux.Session
	idleSince     time.Time // when ss became stream-less (zero = not idle)
	stale         bool      // marked after config reload; evicted when idle
	idleEvicted   bool      // last close was an intentional idle eviction; skip auto-redial
	lastIdentity  string    // peer identity of the most recent session (kept after close)
	closeSig      chan struct{}
	stopOnce      sync.Once
	redialSig     chan struct{}
	redialCount   int
	dialMu        sync.Mutex
	lastChanged   time.Time
	streamLatency latencyRing
}

func newTunnel(dialAddr string, s *Server) *tunnel {
	t := &tunnel{
		dialAddr:  dialAddr,
		s:         s,
		closeSig:  make(chan struct{}),
		redialSig: make(chan struct{}, 1),
	}
	t.tag = t.buildTunnelTag(nil, nil)
	return t
}

func (t *tunnel) getConfig() (*config.File, *tls.Config) {
	return t.s.getConfig()
}

func (t *tunnel) defaultDirectionOutbound() bool {
	return t.dialAddr != ""
}

func resolveMeLabel(identity string, localAddr net.Addr, conn net.Conn) string {
	if identity != "" {
		return identity
	}
	if localAddr == nil && conn != nil {
		localAddr = conn.LocalAddr()
	}
	if localAddr != nil {
		if s := localAddr.String(); s != "" {
			return s
		}
	}
	return "?"
}

func resolvePeerLabel(peerIdentity, peerID string, peerAddr net.Addr, conn net.Conn) string {
	if peerIdentity != "" {
		return peerIdentity
	}
	if peerID != "" {
		return peerID
	}
	if peerAddr == nil && conn != nil {
		peerAddr = conn.RemoteAddr()
	}
	if peerAddr != nil {
		if s := peerAddr.String(); s != "" {
			return s
		}
	}
	return "?"
}

func formatTunnelTag(outbound bool, identity, peerIdentity, peerID string, localAddr, peerAddr net.Addr, conn net.Conn) string {
	arrow := "<="
	if outbound {
		arrow = "=>"
	}
	me := resolveMeLabel(identity, localAddr, conn)
	peer := resolvePeerLabel(peerIdentity, peerID, peerAddr, conn)
	return fmt.Sprintf("%s %s %s", me, arrow, peer)
}

func formatStreamTag(outbound bool, identity, peerIdentity, peerID string, localAddr, peerAddr net.Addr, conn net.Conn) string {
	arrow := "<-"
	if outbound {
		arrow = "->"
	}
	me := resolveMeLabel(identity, localAddr, conn)
	peer := resolvePeerLabel(peerIdentity, peerID, peerAddr, conn)
	return fmt.Sprintf("%s %s %s", me, arrow, peer)
}

func (t *tunnel) buildTunnelTag(ss mux.Session, conn net.Conn) string {
	cfg, _ := t.getConfig()
	peerID := t.dialAddr
	var peerIdentity string
	var localAddr, remoteAddr net.Addr
	if ss != nil {
		peerIdentity = ss.PeerIdentity()
		localAddr = ss.LocalAddr()
		remoteAddr = ss.RemoteAddr()
	}
	return formatTunnelTag(t.defaultDirectionOutbound(), cfg.Identity.Claim, peerIdentity, peerID, localAddr, remoteAddr, conn)
}

func (t *tunnel) updateTagLocked(ss mux.Session, conn net.Conn) {
	t.tag = t.buildTunnelTag(ss, conn)
}

func (t *tunnel) tagValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tag
}

// lastIdentityValue returns the peer identity of the most recent session,
// which is retained after the session closes for dial-on-demand routing.
func (t *tunnel) lastIdentityValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastIdentity
}

// Start launches the outbound redial loop for a config-driven tunnel.
func (t *tunnel) Start() error {
	if t.dialAddr != "" {
		tag := t.tagValue()
		slog.Debugf("%s: start outbound", tag)
		return t.s.g.Go(t.run)
	}
	return nil
}

// Stop closes the current session once and signals the run loop to exit.
// The session is closed but not cleared here: the per-session watcher runs
// finalizeSession, which owns all accounting (numSessions, metrics flush).
func (t *tunnel) Stop() error {
	t.stopOnce.Do(func() {
		close(t.closeSig)
		t.mu.RLock()
		ss := t.ss
		tag := t.tag
		t.mu.RUnlock()
		if ss != nil && !ss.IsClosed() {
			_ = ss.Close()
		}
		slog.Debugf("%s: stop", tag)
	})
	return nil
}

// checkIdle evicts stale or idle sessions by closing them.  It never clears
// t.ss itself: for outbound sessions the per-session watcher runs
// finalizeSession, and for inbound sessions serveSession's defer does the
// accounting, so closing is the only action required here.
func (t *tunnel) checkIdle() {
	cfg, _ := t.getConfig()
	idleTimeout := cfg.IdleTimeout()
	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ss == nil || t.ss.IsClosed() {
		return
	}
	// evict stale sessions immediately when idle (no active streams)
	m := t.ss.Stats()
	var numStreams int64
	if m != nil {
		numStreams = m.NumStreams.Load()
	}
	if t.stale && numStreams == 0 {
		tag := t.tag
		slog.Infof("%s: stale session evicted after reload", tag)
		_ = t.ss.Close()
		return
	}
	// update idle tracking
	if numStreams == 0 {
		if t.idleSince.IsZero() {
			t.idleSince = now
		}
	} else {
		t.idleSince = time.Time{}
	}
	// evict if idle too long
	if idleTimeout > 0 && !t.idleSince.IsZero() && now.Sub(t.idleSince) >= idleTimeout {
		tag := t.tag
		slog.Infof("%s: idle session evicted after %v", tag, now.Sub(t.idleSince))
		// Suppress the automatic redial: the session is intentionally dropped
		// and OpenStream dials on demand when traffic resumes.
		t.idleEvicted = true
		_ = t.ss.Close()
	}
}

func (t *tunnel) redial() {
	ctx := t.s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer t.s.ctx.cancel(ctx)
	_, err := t.dial(ctx)
	if err != nil && !errors.Is(err, ErrNoDialAddress) &&
		!errors.Is(err, ErrDialInProgress) && !errors.Is(err, ErrTunnelStopped) {
		redialCount := t.redialCount + 1
		if redialCount > t.redialCount {
			t.redialCount = redialCount
		}
		tag := t.tagValue()
		slog.Infof("%s: redial #%d to %s: %s", tag, t.redialCount, t.dialAddr, formats.Error(err))
		return
	}
	t.redialCount = 0
}

func (t *tunnel) maintenance() {
	if t.getSession() == nil {
		cfg, _ := t.getConfig()
		if !cfg.NoRedial && t.dialAddr != "" {
			t.redial()
		}
		return
	}
}

func (t *tunnel) schedule() <-chan time.Time {
	cfg, _ := t.getConfig()
	if cfg.NoRedial || t.dialAddr == "" || t.redialCount < 1 {
		// Connected (or no redial needed): sleep until an event wakes the loop.
		// maintenanceLoop handles idle eviction; run() only needs to react to
		// redialSig, closeSig, or group shutdown.
		return nil
	}
	n := t.redialCount - 1
	var waitTimeConst = [...]time.Duration{
		200 * time.Millisecond,
		2 * time.Second,
		2 * time.Second,
		5 * time.Second,
		5 * time.Second,
		15 * time.Second,
		15 * time.Second,
		15 * time.Second,
		1 * time.Minute,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
	}
	waitTime := waitTimeConst[len(waitTimeConst)-1]
	if n < len(waitTimeConst) {
		waitTime = waitTimeConst[n]
	}
	// Apply ±20% jitter to spread reconnect storms.
	waitTime += time.Duration(rand.Int63n(int64(waitTime*2/5))) - waitTime/5
	tag := t.tagValue()
	slog.Debugf("%s: redial scheduled after %v", tag, waitTime)
	return time.After(waitTime)
}

func (t *tunnel) run() {
	defer func() {
		// Close (but do not clear) the session; finalizeSession does the rest.
		t.mu.RLock()
		ss := t.ss
		t.mu.RUnlock()
		if ss != nil && !ss.IsClosed() {
			_ = ss.Close()
		}
	}()
	for {
		t.maintenance()
		select {
		case <-t.closeSig:
			return
		case <-t.redialSig:
		case <-t.schedule():
		case <-t.s.g.CloseC():
			return
		}
	}
}

// watchIdleSession monitors ss for idle-timeout and stale eviction without
// polling.  It relies on ss.IdleChan() which fires each time NumStreams drops
// to zero.  The goroutine exits when the session closes or the group shuts down.
func (t *tunnel) watchIdleSession(ss mux.Session) {
	idleCh := ss.IdleChan()
	if idleCh == nil {
		return // session does not support idle notifications
	}
	closeCh := ss.CloseChan()
	groupCloseCh := t.s.g.CloseC()

	// idle tracks whether we already know NumStreams==0 (idleCh just fired) so
	// we can skip the outer wait and go straight to (re)starting the timer.
	idle := false
	for {
		if !idle {
			select {
			case <-idleCh:
				idle = true
			case <-closeCh:
				return
			case <-groupCloseCh:
				return
			}
		}

		// Immediate check handles stale eviction (config reload case).
		t.checkIdle()
		idle = false
		if ss.IsClosed() {
			return
		}
		// Re-read the timeout each round so config reloads take effect.
		cfg, _ := t.getConfig()
		idleTimeout := cfg.IdleTimeout()
		if idleTimeout <= 0 {
			// No idle timeout configured; only stale eviction needed (handled above).
			continue
		}

		// Wait idleTimeout; reset the timer if another idle event arrives first
		// (meaning streams resumed and dropped again).
		timer := time.NewTimer(idleTimeout)
		select {
		case <-timer.C:
			t.checkIdle()
			// Whether evicted or not, loop back and wait for the next idle event.
		case <-idleCh:
			if !timer.Stop() {
				<-timer.C
			}
			// Streams resumed and became idle again; skip outer wait, restart timer.
			idle = true
		case <-closeCh:
			timer.Stop()
			return
		case <-groupCloseCh:
			timer.Stop()
			return
		}
	}
}

// addSession installs ss as the tunnel's active session and registers the
// idle watcher.  It returns false (closing nothing) when the tunnel has been
// stopped, in which case the caller must close ss itself.  Every session
// accepted here is finalized exactly once by finalizeSession.
func (t *tunnel) addSession(ss mux.Session, setupDur time.Duration) bool {
	now := time.Now()
	t.mu.Lock()
	select {
	case <-t.closeSig:
		// Stop() won the race against an in-flight dial; reject the session.
		t.mu.Unlock()
		return false
	default:
	}
	if t.ss != nil && !t.ss.IsClosed() {
		// Should not happen (dials are serialized); let its watcher finalize it.
		_ = t.ss.Close()
	}
	t.ss = ss
	t.updateTagLocked(ss, nil)
	tag := t.tag
	t.stale = false
	t.idleEvicted = false
	t.idleSince = time.Time{}
	t.lastIdentity = ss.PeerIdentity()
	t.lastChanged = now
	t.mu.Unlock()
	t.s.stats.numSessions.Add(1)
	t.s.stats.numSessionsCreated.Add(1)

	msg := fmt.Sprintf("%s: session established (setup: %s)", tag, formats.Duration(setupDur))
	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)
	_ = t.s.g.Go(func() { t.watchIdleSession(ss) })
	return true
}

// finalizeSession does the one-time accounting for a closed session: flushing
// its metrics, decrementing counters, logging, and (when ss is still the
// tunnel's current session) clearing tunnel state and scheduling a redial.
// It must be called exactly once per session installed by addSession.
func (t *tunnel) finalizeSession(ss mux.Session) {
	now := time.Now()
	t.mu.Lock()
	current := t.ss == ss
	idleEvicted := t.idleEvicted
	if current {
		t.ss = nil
		t.idleSince = time.Time{}
		t.idleEvicted = false
		t.lastChanged = now
	}
	tag := t.tag
	if current && !idleEvicted && t.dialAddr != "" {
		select {
		case t.redialSig <- struct{}{}:
		default:
		}
	}
	t.mu.Unlock()

	t.s.flushSessionMetrics(ss)
	t.s.stats.numSessions.Add(^uint32(0))
	t.s.stats.numSessionsFinalized.Add(1)
	msg := fmt.Sprintf("%s: session closed", tag)
	slog.Notice(msg)
	t.s.recentEvents.Add(now, msg)
}

func (t *tunnel) getSession() mux.Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.ss == nil || t.ss.IsClosed() {
		return nil
	}
	return t.ss
}

// OpenStream opens a stream over the active session.  When no session is
// active (e.g. after an idle eviction or before the redial loop reconnects),
// it dials a new session on demand.  Dial-on-demand applies regardless of
// NoRedial, which only disables the background redial loop.
func (t *tunnel) OpenStream(ctx context.Context) (net.Conn, error) {
	ss := t.getSession()
	if ss == nil {
		if t.dialAddr == "" {
			return nil, ErrNoSession
		}
		var err error
		if ss, err = t.dial(ctx); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	conn, err := ss.Open(ctx)
	if err != nil {
		return nil, err
	}
	t.streamLatency.Record(time.Since(start))
	return conn, nil
}

// dial establishes a new outbound mux session.
func (t *tunnel) dial(ctx context.Context) (mux.Session, error) {
	_, tlscfg := t.getConfig()
	if t.dialAddr == "" {
		return nil, ErrNoDialAddress
	}
	if !t.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer t.dialMu.Unlock()
	if tlscfg == nil {
		slog.Warningf("%s: connection is not encrypted", t.tagValue())
	}
	start := time.Now()
	ss, err := t.s.getMuxDialer().Dial(ctx, t.dialAddr)
	if err != nil {
		return nil, err
	}
	if !t.addSession(ss, time.Since(start)) {
		_ = ss.Close()
		return nil, ErrTunnelStopped
	}
	// Finalize the session exactly once when it closes (accounting + redial).
	if err := t.s.g.Go(func() {
		<-ss.CloseChan()
		t.finalizeSession(ss)
	}); err != nil {
		_ = ss.Close()
		t.finalizeSession(ss)
		return nil, err
	}
	// Close ss when the group shuts down, to unblock the accept loop.
	// This is needed in particular after a redial, where t.ss has moved on
	// and t.run's defer will no longer close this specific ss.
	if err := t.s.g.Go(func() {
		select {
		case <-t.s.g.CloseC():
			_ = ss.Close()
		case <-ss.CloseChan():
		}
	}); err != nil {
		_ = ss.Close()
		return nil, err
	}
	// Accept server-initiated streams so that dialStreamForServer conns do not
	// pile up in acceptCh and numStreams never decrements (the stream leak).
	if err := t.s.g.Go(func() {
		t.s.acceptInboundStreams(t, ss)
	}); err != nil {
		_ = ss.Close()
		return nil, err
	}

	return ss, nil
}

// SessionStats snapshots the most recent session state for one tunnel key.
type SessionStats struct {
	PeerIdentity string
	LastChanged  time.Time
	Active       bool
	// gRPC transport statistics; zero when unavailable.
	StreamsOpened      uint64
	StreamsAccepted    uint64
	StreamsSucceeded   uint64
	StreamsFailed      uint64
	NumStreams         uint32
	BytesSent          uint64
	BytesReceived      uint64
	WireLengthSent     uint64
	WireLengthReceived uint64
	// StreamLatency holds pre-computed percentiles of this tunnel's
	// stream-open latency ring.
	StreamLatency StreamLatencyStats
}

// Stats snapshots the current session state.
func (t *tunnel) Stats() SessionStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	active := t.ss != nil && !t.ss.IsClosed()
	var peerIdentity string
	var streamsOpened, streamsAccepted, streamsSucceeded, streamsFailed, bytesSent, bytesReceived, wireLengthSent, wireLengthReceived uint64
	var numStreams uint32
	if active {
		peerIdentity = t.ss.PeerIdentity()
		if m := t.ss.Stats(); m != nil {
			streamsOpened = uint64(m.StreamsOpened.Load())
			streamsAccepted = uint64(m.StreamsAccepted.Load())
			streamsSucceeded = uint64(m.StreamsSucceeded.Load())
			streamsFailed = uint64(m.StreamsFailed.Load())
			numStreams = uint32(m.NumStreams.Load())
			bytesSent = uint64(m.BytesSent.Load())
			bytesReceived = uint64(m.BytesReceived.Load())
			wireLengthSent = uint64(m.WireLengthSent.Load())
			wireLengthReceived = uint64(m.WireLengthReceived.Load())
		}
	}
	p50, p90, p99, pmax, latOk := t.streamLatency.Percentiles()
	return SessionStats{
		PeerIdentity:       peerIdentity,
		LastChanged:        t.lastChanged,
		Active:             active,
		StreamsOpened:      streamsOpened,
		StreamsAccepted:    streamsAccepted,
		StreamsSucceeded:   streamsSucceeded,
		StreamsFailed:      streamsFailed,
		NumStreams:         numStreams,
		BytesSent:          bytesSent,
		BytesReceived:      bytesReceived,
		WireLengthSent:     wireLengthSent,
		WireLengthReceived: wireLengthReceived,
		StreamLatency:      StreamLatencyStats{P50: p50, P90: p90, P99: p99, Max: pmax, Available: latOk},
	}
}
