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
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3/proto"
)

type Tunnel struct {
	peerName    string // used for logging
	s           *Server
	c           *TunnelConfig
	mu          sync.RWMutex
	mux         map[*yamux.Session]string // map[mux]tag
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func NewTunnel(s *Server, peerName string, c *TunnelConfig) *Tunnel {
	return &Tunnel{
		peerName: peerName, s: s, c: c,
		mux:       make(map[*yamux.Session]string),
		redialSig: make(chan struct{}, 1),
	}
}

func (t *Tunnel) Start() error {
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
		slog.Warningf("tunnel %q: redial #%d to %s: %s", t.peerName, t.redialCount, t.c.MuxDial, formats.Error(err))
		return
	}
	t.redialCount = 0
}

func (t *Tunnel) scheduleRedial() <-chan time.Time {
	if !t.c.Redial || t.c.MuxDial == "" || t.redialCount < 1 {
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
	slog.Debugf("tunnel %q: redial scheduled after %v", t.peerName, waitTime)
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
		tag = fmt.Sprintf("%q => %v", t.peerName, mux.RemoteAddr())
	} else {
		tag = fmt.Sprintf("%q <= %v", t.peerName, mux.RemoteAddr())
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
		slog.Warningf("%q => %v: connection is not encrypted", t.peerName, conn.RemoteAddr())
	}
	req := &proto.Message{
		Type:     proto.Type,
		Msg:      proto.MsgClientHello,
		PeerName: t.s.c.PeerName,
		Service:  t.c.PeerService,
	}
	rsp, err := proto.Roundtrip(conn, req)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Client(conn, t.c.NewMuxConfig(t.s.c))
	if err != nil {
		return nil, err
	}
	t.addMux(mux, true)
	var muxHandler Handler
	if dialAddr, ok := c.Services[req.Service]; ok {
		muxHandler = &ForwardHandler{
			s: t.s, tag: t.peerName, dial: dialAddr,
		}
	}
	if muxHandler == nil {
		if req.Service != "" {
			slog.Infof("%q => %v: unknown service %q", t.peerName, conn.RemoteAddr(), rsp.Service)
		}
		if err := mux.GoAway(); err != nil {
			return nil, err
		}
		muxHandler = &EmptyHandler{}
	}
	if err := t.s.g.Go(func() {
		defer t.delMux(mux)
		t.s.Serve(mux, muxHandler)
	}); err != nil {
		ioClose(mux)
		return nil, err
	}
	slog.Infof("%q => %v: setup %v", t.peerName, conn.RemoteAddr(), formats.Duration(time.Since(start)))
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
	slog.Debugf("stream open: %q ID=%v", t.peerName, stream.StreamID())
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
		Name:        t.peerName,
		LastChanged: t.lastChanged,
		NumSessions: numSessions,
		NumStreams:  numStreams,
	}
}
