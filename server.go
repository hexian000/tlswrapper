package tlswrapper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/forwarder"
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
	mu sync.Mutex

	c            *Config
	tlscfg       *tls.Config
	muxcfg       *yamux.Config
	servermuxcfg *yamux.Config

	f forwarder.Forwarder

	listeners map[string]net.Listener
	tunnels   map[string]*Tunnel
	contexts  map[context.Context]context.CancelFunc

	dialer net.Dialer
	g      routines.Group
}

// NewServer creates a server object
func NewServer(cfg *Config) *Server {
	g := routines.NewGroup(65536)
	return &Server{
		listeners: make(map[string]net.Listener),
		tunnels:   make(map[string]*Tunnel),
		contexts:  make(map[context.Context]context.CancelFunc),
		f:         forwarder.New(cfg.MaxConn, g),
		g:         g,
		c:         cfg,
	}
}

func (s *Server) withTimeout() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contexts == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.c.Timeout())
	s.contexts[ctx] = cancel
	return ctx
}

func (s *Server) cancel(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contexts == nil {
		return
	}
	if cancel, ok := s.contexts[ctx]; ok {
		cancel()
		delete(s.contexts, ctx)
	}
}

func (s *Server) addTunnel(name string, c *TunnelConfig) *Tunnel {
	t := NewTunnel(name, s, c)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnels[name] = t
	return t
}

func (s *Server) dialDirect(ctx context.Context, addr string) (net.Conn, error) {
	slog.Debug("forward to:", addr)
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
	ctx := s.withTimeout()
	if ctx == nil {
		return
	}
	defer s.cancel(ctx)
	handler.Serve(ctx, accepted)
}

func (s *Server) Serve(listener net.Listener, handler Handler) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) ||
				errors.Is(err, yamux.ErrSessionShutdown) {
				return
			}
			slog.Errorf("accept: %v", err)
			continue
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
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, cancel := range s.contexts {
			cancel()
		}
		s.contexts = nil
	}()
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
