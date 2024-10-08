package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/gosnippets/formats"
	snet "github.com/hexian000/gosnippets/net"
	"github.com/hexian000/gosnippets/net/hlistener"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v2/proto"
)

type Tunnel struct {
	name        string // used for logging
	s           *Server
	c           *TunnelConfig
	l           hlistener.Listener
	mu          sync.RWMutex
	mux         map[*yamux.Session]string // map[mux]tag
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func NewTunnel(s *Server, c *TunnelConfig) *Tunnel {
	return &Tunnel{
		name:      c.Identity,
		s:         s,
		c:         c,
		mux:       make(map[*yamux.Session]string),
		redialSig: make(chan struct{}, 1),
	}
}

func (t *Tunnel) Start() error {
	if t.c.MuxListen != "" {
		l, err := t.s.Listen(t.c.MuxListen)
		if err != nil {
			return err
		}
		slog.Noticef("mux listen: %v", l.Addr())
		h := &TLSHandler{s: t.s, t: t}
		c := t.s.getConfig()
		t.l = hlistener.Wrap(l, &hlistener.Config{
			Start:       uint32(c.StartupLimitStart),
			Full:        uint32(c.StartupLimitFull),
			Rate:        float64(c.StartupLimitRate) / 100.0,
			MaxSessions: uint32(c.MaxSessions),
			Stats:       h.Stats4Listener,
		})
		l = t.l
		if err := t.s.g.Go(func() {
			t.s.Serve(l, h)
		}); err != nil {
			return err
		}
	}
	if t.c.Listen != "" {
		l, err := t.s.Listen(t.c.Listen)
		if err != nil {
			return err
		}
		slog.Noticef("forward listen: %v", l.Addr())
		h := &TunnelHandler{l: l, s: t.s, t: t}
		if err := t.s.g.Go(func() {
			t.s.Serve(l, h)
		}); err != nil {
			return err
		}
	}
	return t.s.g.Go(t.run)
}

func (t *Tunnel) redial() {
	ctx := t.s.ctx.withTimeout()
	if ctx == nil {
		return
	}
	defer t.s.ctx.cancel(ctx)
	_, err := t.dial(ctx)
	if err != nil && !errors.Is(err, ErrNoDialAddress) && !errors.Is(err, ErrDialInProgress) {
		redialCount := t.redialCount + 1
		if redialCount > t.redialCount {
			t.redialCount = redialCount
		}
		slog.Warningf("tunnel %q: redial #%d to %s: %s", t.name, t.redialCount, t.c.MuxDial, formats.Error(err))
		return
	}
	t.redialCount = 0
}

func (t *Tunnel) scheduleRedial() <-chan time.Time {
	if !t.s.c.Redial || t.c.MuxDial == "" || t.redialCount < 1 {
		return make(<-chan time.Time)
	}
	n := t.redialCount - 1
	var waitTimeConst = [...]time.Duration{
		200 * time.Millisecond,
		2 * time.Second,
		2 * time.Second,
		5 * time.Second,
		5 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
	}
	waitTime := waitTimeConst[len(waitTimeConst)-1]
	if n < len(waitTimeConst) {
		waitTime = waitTimeConst[n]
	}
	slog.Debugf("tunnel %q: redial scheduled after %v", t.name, waitTime)
	return time.After(waitTime)
}

func (t *Tunnel) run() {
	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for mux := range t.mux {
			ioClose(mux)
			delete(t.mux, mux)
		}
	}()
	for {
		t.redial()
		select {
		case <-t.redialSig:
		case <-t.scheduleRedial():
		case <-t.s.g.CloseC():
			// server shutdown
			return
		}
	}
}

func (t *Tunnel) addMux(mux *yamux.Session, isDialed bool) {
	now := time.Now()
	var tag string
	if isDialed {
		tag = fmt.Sprintf("%q => %v", t.name, mux.RemoteAddr())
	} else {
		tag = fmt.Sprintf("%q <= %v", t.name, mux.RemoteAddr())
	}
	msg := fmt.Sprintf("%s: established", tag)
	slog.Info(msg)
	t.s.recentEvents.Add(now, msg)

	t.mu.Lock()
	defer t.mu.Unlock()
	num := len(t.mux)
	for mux := range t.mux {
		if mux.IsClosed() {
			delete(t.mux, mux)
		}
	}
	t.mux[mux] = tag
	t.s.numSessions.Add(uint32(len(t.mux) - num))
	t.lastChanged = now
}

func (t *Tunnel) getMuxTag(mux *yamux.Session) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tag, ok := t.mux[mux]
	return tag, ok
}

func (t *Tunnel) delMux(mux *yamux.Session) {
	now := time.Now()
	if tag, ok := t.getMuxTag(mux); ok {
		msg := fmt.Sprintf("%s: connection lost", tag)
		slog.Info(msg)
		t.s.recentEvents.Add(now, msg)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	num := len(t.mux)
	delete(t.mux, mux)
	for mux := range t.mux {
		if mux.IsClosed() {
			delete(t.mux, mux)
		}
	}
	t.s.numSessions.Add(uint32(len(t.mux) - num))
	t.lastChanged = now
	select {
	case t.redialSig <- struct{}{}:
	default:
	}
}

func (t *Tunnel) getMux() *yamux.Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for mux := range t.mux {
		if !mux.IsClosed() {
			return mux
		}
	}
	return nil
}

func (t *Tunnel) NumSessions() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.mux)
}

func (t *Tunnel) Serve(mux *yamux.Session) {
	var h Handler
	if t.c.Dial != "" {
		h = &ForwardHandler{
			t.s, t.name, t.c.Dial,
		}
	} else {
		h = &EmptyHandler{}
	}
	t.s.Serve(mux, h)
}

func (t *Tunnel) dial(ctx context.Context) (*yamux.Session, error) {
	if !t.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer t.dialMu.Unlock()
	if mux := t.getMux(); mux != nil {
		return mux, nil
	}
	if t.c.MuxDial == "" {
		return nil, ErrNoDialAddress
	}
	start := time.Now()
	conn, err := t.s.dialer.DialContext(ctx, network, t.c.MuxDial)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	c := t.s.getConfig()
	c.SetConnParams(conn)
	conn = snet.FlowMeter(conn, t.s.flowStats)
	if tlscfg := t.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Client(conn, tlscfg)
	} else {
		slog.Warningf("%q => %v: connection is not encrypted", t.name, conn.RemoteAddr())
	}
	handshake := &proto.Handshake{
		Identity: c.Identity,
	}
	if t.c.LocalIdentity != "" {
		handshake.Identity = t.c.LocalIdentity
	}
	if err := proto.RunHandshake(conn, handshake); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Client(conn, t.s.getMuxConfig(false))
	if err != nil {
		return nil, err
	}
	tun := t
	if handshake.Identity != "" {
		if found := t.s.findTunnel(handshake.Identity); found != nil {
			tun = found
		} else {
			slog.Infof("%q => %v: unknown identity %q", t.name, conn.RemoteAddr(), handshake.Identity)
		}
	}
	t.addMux(mux, true)
	if err := t.s.g.Go(func() {
		defer t.delMux(mux)
		tun.Serve(mux)
	}); err != nil {
		ioClose(mux)
		return nil, err
	}
	slog.Infof("%q => %v: setup %v", t.name, conn.RemoteAddr(), formats.Duration(time.Since(start)))
	return mux, nil
}

func (t *Tunnel) MuxDial(ctx context.Context) (net.Conn, error) {
	mux := t.getMux()
	if mux == nil {
		var err error
		if mux, err = t.dial(ctx); err != nil {
			return nil, err
		}
	}
	stream, err := mux.OpenStream()
	if err != nil {
		return nil, err
	}
	slog.Debugf("stream open: %q ID=%v", t.name, stream.StreamID())
	return stream, nil
}

type TunnelStats struct {
	Name        string
	LastChanged time.Time
	NumSessions int
	NumStreams  int
}

func (t *Tunnel) Stats() TunnelStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	numSessions, numStreams := 0, 0
	for mux := range t.mux {
		if !mux.IsClosed() {
			numSessions++
			numStreams += mux.NumStreams()
		}
	}
	return TunnelStats{
		Name:        t.name,
		LastChanged: t.lastChanged,
		NumSessions: numSessions,
		NumStreams:  numStreams,
	}
}
