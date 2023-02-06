package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/hlistener"
	"github.com/hexian000/tlswrapper/slog"
)

type Tunnel struct {
	name   string
	s      *Server
	c      *TunnelConfig
	mu     sync.RWMutex
	mux    map[*yamux.Session]struct{}
	dialMu sync.Mutex
}

func NewTunnel(name string, s *Server, c *TunnelConfig) *Tunnel {
	return &Tunnel{
		name: name,
		s:    s,
		c:    c,
		mux:  make(map[*yamux.Session]struct{}),
	}
}

func (t *Tunnel) Start() error {
	if t.c.MuxListen != "" {
		l, err := t.s.Listen(t.c.MuxListen)
		if err != nil {
			return err
		}
		h := &TLSHandler{s: t.s, t: t}
		l = hlistener.Wrap(l, &hlistener.Config{
			Start:        uint32(t.s.c.StartupLimitStart),
			Full:         uint32(t.s.c.StartupLimitFull),
			Rate:         float64(t.s.c.StartupLimitRate) / 100.0,
			Unauthorized: h.Unauthorized,
		})
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

func (t *Tunnel) run() {
	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for mux := range t.mux {
			_ = mux.Close()
			delete(t.mux, mux)
		}
	}()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	redial := func() {
		ctx := t.s.withTimeout()
		if ctx == nil {
			return
		}
		defer t.s.cancel(ctx)
		_, err := t.dialMux(ctx)
		if err != nil && !errors.Is(err, ErrNoSession) {
			slog.Warning("redial:", err)
		}
	}
	redial()
	for {
		select {
		case <-t.s.g.CloseC():
			return
		case <-ticker.C:
		}
		redial()
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

func (t *Tunnel) NumSessions() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for mux := range t.mux {
		if !mux.IsClosed() {
			n++
		}
	}
	return n
}

func (t *Tunnel) dialMux(ctx context.Context) (*yamux.Session, error) {
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
	t.s.c.SetConnParams(conn)
	if t.s.tlscfg != nil {
		tlsConn := tls.Client(conn, t.s.tlscfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	} else {
		slog.Warning("connection is not encrypted")
	}
	mux, err := yamux.Client(conn, t.s.muxcfg)
	if err != nil {
		return nil, err
	}
	if t.c.Dial != "" {
		if err := t.s.g.Go(func() {
			t.s.Serve(mux, &ForwardHandler{
				t.s,
				t.c.Dial,
			})
		}); err != nil {
			_ = mux.Close()
			return nil, err
		}
	}
	slog.Info("session dial:", conn.RemoteAddr(), "setup:", time.Since(start))
	t.addMux(mux)
	return mux, nil
}

func (t *Tunnel) MuxDial(ctx context.Context) (net.Conn, error) {
	mux, err := t.dialMux(ctx)
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
