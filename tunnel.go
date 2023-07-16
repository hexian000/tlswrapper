package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/formats"
	"github.com/hexian000/tlswrapper/hlistener"
	"github.com/hexian000/tlswrapper/meter"
	"github.com/hexian000/tlswrapper/proto"
	"github.com/hexian000/tlswrapper/slog"
)

type Tunnel struct {
	name        string // used for logging
	s           *Server
	c           *TunnelConfig
	l           *hlistener.Listener
	mu          sync.RWMutex
	mux         map[*yamux.Session]struct{}
	muxCloseSig chan *yamux.Session
	redialCount int
	dialMu      sync.Mutex
}

func NewTunnel(s *Server, c *TunnelConfig) *Tunnel {
	return &Tunnel{
		name:        c.Identity,
		s:           s,
		c:           c,
		mux:         make(map[*yamux.Session]struct{}),
		muxCloseSig: make(chan *yamux.Session, 16),
	}
}

func (t *Tunnel) Start() error {
	if t.c.MuxListen != "" {
		l, err := t.s.Listen(t.c.MuxListen)
		if err != nil {
			return err
		}
		h := &TLSHandler{s: t.s, t: t}
		c := t.s.getConfig()
		t.l = hlistener.Wrap(l, &hlistener.Config{
			Start:        uint32(c.StartupLimitStart),
			Full:         uint32(c.StartupLimitFull),
			Rate:         float64(c.StartupLimitRate) / 100.0,
			Unauthorized: h.Unauthorized,
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
		h := &TunnelHandler{s: t.s, t: t}
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
	if err != nil && !errors.Is(err, ErrNoSession) {
		t.redialCount++
		slog.Warningf("redial #%d: (%T) %v", t.redialCount, err, err)
		return
	}
	t.redialCount = 0
}

func (t *Tunnel) scheduleRedial() <-chan time.Time {
	n := t.redialCount - 1
	if n < 0 {
		return make(<-chan time.Time)
	}
	var waitTimeConst = [...]time.Duration{
		10 * time.Second,
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
	}
	waitTime := 30 * time.Minute
	if n < len(waitTimeConst) {
		waitTime = waitTimeConst[n]
	}
	slog.Debugf("redial: scheduled after %v", waitTime)
	return time.After(waitTime)
}

func (t *Tunnel) run() {
	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for mux := range t.mux {
			_ = mux.Close()
			delete(t.mux, mux)
		}
	}()
	for {
		t.redial()
		select {
		case mux := <-t.muxCloseSig:
			slog.Infof("tunnel %q: connection lost %v", t.name, mux.RemoteAddr())
		case <-t.scheduleRedial():
		case <-t.s.g.CloseC():
			// server shutdown
			return
		}
	}
}

func (t *Tunnel) addMux(mux *yamux.Session) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for mux := range t.mux {
		if mux.IsClosed() {
			delete(t.mux, mux)
		}
	}
	t.mux[mux] = struct{}{}
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

func (t *Tunnel) NumSessions() (num int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for mux := range t.mux {
		if !mux.IsClosed() {
			num++
		}
	}
	return
}

func (t *Tunnel) NumStreams() (num int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for mux := range t.mux {
		if !mux.IsClosed() {
			num += mux.NumStreams()
		}
	}
	return
}

func (t *Tunnel) Serve(mux *yamux.Session) {
	var h Handler
	if t.c.Dial != "" {
		h = &ForwardHandler{
			t.s,
			t.c.Dial,
		}
	} else {
		h = &EmptyHandler{}
	}
	t.addMux(mux)
	t.s.Serve(mux, h)
	t.muxCloseSig <- mux
}

func (t *Tunnel) dial(ctx context.Context) (*yamux.Session, error) {
	if mux := t.getMux(); mux != nil {
		return mux, nil
	}
	if t.c.MuxDial == "" {
		return nil, ErrNoSession
	}
	if !t.dialMu.TryLock() {
		return nil, ErrNoSession
	}
	defer t.dialMu.Unlock()
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
	conn = meter.Conn(conn, t.s.meter)
	if tlscfg := t.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Client(conn, tlscfg)
	} else {
		slog.Warningf("tunnel %q: connection is not encrypted", t.name)
	}
	handshake := &proto.Handshake{
		Identity: c.Identity,
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
			slog.Warningf("unknown remote identity %q", handshake.Identity)
		}
	}
	if err := t.s.g.Go(func() {
		tun.Serve(mux)
	}); err != nil {
		_ = mux.Close()
		return nil, err
	}
	slog.Infof("tunnel %q: dial %v, setup: %v", t.name, conn.RemoteAddr(), formats.Duration(time.Since(start)))
	return mux, nil
}

func (t *Tunnel) MuxDial(ctx context.Context) (net.Conn, error) {
	mux, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := mux.OpenStream()
	if err != nil {
		return nil, err
	}
	slog.Debugf("stream open: %s ID=%v", t.name, stream.StreamID())
	return stream, nil
}
