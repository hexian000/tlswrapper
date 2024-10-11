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
	"github.com/hexian000/tlswrapper/v3/config"
	"github.com/hexian000/tlswrapper/v3/proto"
)

type tunnel struct {
	peerName    string // used for logging
	s           *Server
	mu          sync.RWMutex
	mux         map[*yamux.Session]string // map[mux]tag
	redialSig   chan struct{}
	redialCount int
	dialMu      sync.Mutex
	lastChanged time.Time
}

func (t *tunnel) getConfig() *config.Tunnel {
	cfg := t.s.getConfig()
	return cfg.GetTunnel(t.peerName)
}

func (t *tunnel) Start() error {
	c := t.getConfig()
	if c.Listen != "" {
		l, err := t.s.Listen(c.Listen)
		if err != nil {
			return err
		}
		slog.Noticef("tunnel %q: listen %v", t.peerName, l.Addr())
		h := &TunnelHandler{l: l, s: t.s, t: t}
		if err := t.s.g.Go(func() {
			t.s.Serve(l, h)
		}); err != nil {
			return err
		}
	}
	return t.s.g.Go(t.run)
}

func (t *tunnel) redial() {
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
		c := t.getConfig()
		slog.Warningf("tunnel %q: redial #%d to %s: %s", t.peerName, t.redialCount, c.MuxDial, formats.Error(err))
		return
	}
	t.redialCount = 0
}

func (t *tunnel) scheduleRedial() <-chan time.Time {
	c := t.getConfig()
	if !c.NoRedial || c.MuxDial == "" || t.redialCount < 1 {
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

func (t *tunnel) run() {
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

func (t *tunnel) addMux(mux *yamux.Session, isDialed bool) {
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

func (t *tunnel) getMuxTag(mux *yamux.Session) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tag, ok := t.mux[mux]
	return tag, ok
}

func (t *tunnel) delMux(mux *yamux.Session) {
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

func (t *tunnel) getMux() *yamux.Session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for mux := range t.mux {
		if !mux.IsClosed() {
			return mux
		}
	}
	return nil
}

func (t *tunnel) NumSessions() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.mux)
}

func (t *tunnel) dial(ctx context.Context) (*yamux.Session, error) {
	if !t.dialMu.TryLock() {
		return nil, ErrDialInProgress
	}
	defer t.dialMu.Unlock()
	if mux := t.getMux(); mux != nil {
		return mux, nil
	}
	tuncfg := t.getConfig()
	if tuncfg.MuxDial == "" {
		return nil, ErrNoDialAddress
	}
	start := time.Now()
	conn, err := t.s.dialer.DialContext(ctx, network, tuncfg.MuxDial)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	cfg := t.s.getConfig()
	cfg.SetConnParams(conn)
	conn = snet.FlowMeter(conn, t.s.flowStats)
	if tlscfg := t.s.getTLSConfig(); tlscfg != nil {
		conn = tls.Client(conn, tlscfg)
	} else {
		slog.Warningf("%q => %v: connection is not encrypted", t.peerName, conn.RemoteAddr())
	}
	req := &proto.Message{
		Type:     proto.Type,
		Msg:      proto.MsgClientHello,
		PeerName: cfg.PeerName,
		Service:  tuncfg.PeerService,
	}
	rsp, err := proto.Roundtrip(conn, req)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	mux, err := t.s.startMux(conn, rsp.PeerName, rsp.Service, true)
	if err != nil {
		return nil, err
	}
	slog.Debugf("%q => %v: service=%q, setup %v", rsp.PeerName, conn.RemoteAddr(),
		rsp.Service, formats.Duration(time.Since(start)))
	return mux, nil
}

func (t *tunnel) MuxDial(ctx context.Context) (net.Conn, error) {
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

func (t *tunnel) Stats() TunnelStats {
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
