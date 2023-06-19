package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/forwarder"
	"github.com/hexian000/tlswrapper/hlistener"
	"github.com/hexian000/tlswrapper/meter"
	"github.com/hexian000/tlswrapper/routines"
	"github.com/hexian000/tlswrapper/slog"
)

const network = "tcp"

var (
	ErrShutdown  = errors.New("server is shutting down")
	ErrNoSession = errors.New("no session available")
)

// Server object
type Server struct {
	c            *Config
	tlscfg       *tls.Config
	muxcfg       *yamux.Config
	servermuxcfg *yamux.Config

	f      forwarder.Forwarder
	meter  *meter.ConnMetrics
	lstats *hlistener.Stats

	listeners map[string]net.Listener
	tunnels   map[string]*Tunnel
	tunnelsMu sync.Mutex
	ctx       contextMgr

	dialer net.Dialer
	g      routines.Group
}

// NewServer creates a server object
func NewServer(cfg *Config) *Server {
	g := routines.NewGroup(65536)
	return &Server{
		listeners: make(map[string]net.Listener),
		tunnels:   make(map[string]*Tunnel),
		ctx: contextMgr{
			timeout:  cfg.Timeout,
			contexts: make(map[context.Context]context.CancelFunc),
		},
		f:      forwarder.New(cfg.MaxConn, g),
		meter:  &meter.ConnMetrics{},
		lstats: &hlistener.Stats{},
		g:      g,
		c:      cfg,
	}
}

func (s *Server) addTunnel(name string, c *TunnelConfig) *Tunnel {
	t := NewTunnel(name, s, c)
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	s.tunnels[name] = t
	return t
}

func (s *Server) getTunnels() []*Tunnel {
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	tunnels := make([]*Tunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		tunnels = append(tunnels, t)
	}
	return tunnels
}

func (s *Server) NumSessions() int {
	num := 0
	for _, t := range s.getTunnels() {
		num += t.NumSessions()
	}
	return num
}

func (s *Server) CountBytes() (read uint64, written uint64) {
	read = atomic.LoadUint64(&s.meter.Read)
	written = atomic.LoadUint64(&s.meter.Written)
	return
}

func (s *Server) CountConns() (accepted uint64, refused uint64) {
	accepted = atomic.LoadUint64(&s.lstats.Accepted)
	refused = atomic.LoadUint64(&s.lstats.Refused)
	return
}

func (s *Server) dialDirect(ctx context.Context, addr string) (net.Conn, error) {
	slog.Verbose("forward to:", addr)
	dialed, err := s.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return dialed, nil
}

func (s *Server) serveOne(accepted net.Conn, handler Handler) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic:", r, string(debug.Stack()))
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
			slog.Errorf("accept: %v", err)
			return
		}
		s.serveOne(conn, handler)
	}
}

func (s *Server) Listen(addr string) (net.Listener, error) {
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Error("listen", addr, ":", err)
		return listener, err
	}
	slog.Info("listen:", listener.Addr())
	s.listeners[addr] = listener
	return listener, err
}

// Start the service
func (s *Server) Start() error {
	if s.c.HTTPListen != "" {
		l, err := s.Listen(s.c.HTTPListen)
		if err != nil {
			return err
		}
		if err := s.g.Go(func() {
			RunHTTPServer(l, s)
		}); err != nil {
			return err
		}
	}
	for i, c := range s.c.Tunnels {
		name := c.MuxListen
		if name == "" {
			name = c.MuxDial
		}
		name = fmt.Sprintf("[%d] %s", i, name)
		t := s.addTunnel(name, &s.c.Tunnels[i])
		slog.Verbosef("start tunnel: %s", t.name)
		if err := t.Start(); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown gracefully
func (s *Server) Shutdown() error {
	for addr, listener := range s.listeners {
		slog.Info("listener close:", listener.Addr())
		_ = listener.Close()
		delete(s.listeners, addr)
	}
	s.ctx.close()
	s.f.Close()
	s.g.Close()
	slog.Info("waiting for unfinished connections")
	s.g.Wait()
	return nil
}

// Load or reload configuration
func (s *Server) LoadConfig(cfg *Config) error {
	if s.c != nil {
		if !reflect.DeepEqual(s.c.Tunnels, cfg.Tunnels) {
			slog.Warning("tunnel changes are ignored")
		}
	}
	tlscfg, err := cfg.NewTLSConfig(cfg.ServerName)
	if err != nil {
		return err
	}
	s.c = cfg
	s.tlscfg = tlscfg
	s.muxcfg = cfg.NewMuxConfig(false)
	s.servermuxcfg = cfg.NewMuxConfig(true)
	return nil
}
