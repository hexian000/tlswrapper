package main

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexian000/tlswrapper/forwarder"
	"github.com/hexian000/tlswrapper/hlistener"
	"github.com/hexian000/tlswrapper/session"
	"github.com/hexian000/tlswrapper/slog"
)

const network = "tcp"

type Service struct {
	mu        sync.RWMutex
	cfg       *Config
	serverCfg *session.Config
	clientCfg *session.Config
	f         *forwarder.Forwarder
	sessions  []*session.Session
	contexts  map[context.Context]context.CancelFunc

	tlsListener  net.Listener
	tcpListener  net.Listener
	unauthorized uint32

	current     int
	redialSig   chan struct{}
	shutdownSig chan struct{}
}

func NewService(cfg *Config) (*Service, error) {
	tls, err := cfg.LoadTLSConfig()
	if err != nil {
		return nil, err
	}
	s := &Service{
		cfg:         cfg,
		serverCfg:   &session.Config{TLS: tls, Mux: cfg.NewMuxConfig(true)},
		clientCfg:   &session.Config{TLS: tls, Mux: cfg.NewMuxConfig(false)},
		f:           forwarder.New(),
		sessions:    make([]*session.Session, 0),
		contexts:    make(map[context.Context]context.CancelFunc),
		redialSig:   make(chan struct{}, 1),
		shutdownSig: make(chan struct{}),
	}
	return s, nil
}

func (s *Service) addSession(ss *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, ss)
}

func (s *Service) deleteSession(ss *session.Session) {
	_ = ss.Close()
	sessions := make([]*session.Session, 0, len(s.sessions))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.sessions {
		if it != ss {
			sessions = append(sessions, it)
		}
	}
	s.sessions = sessions
	s.notifyRedial()
}

func (s *Service) numSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func (s *Service) notifyRedial() {
	select {
	case s.redialSig <- struct{}{}:
	default:
	}
}

func (s *Service) serveSession(ss *session.Session) {
	defer s.deleteSession(ss)
	for {
		accepted, err := ss.Accept()
		if err != nil {
			slog.Error(err)
			return
		}
		go s.dialLocal(accepted)
	}
}

func (s *Service) dialLocal(accepted net.Conn) {
	timeout := time.Duration(s.cfg.Local.DialTimeout) * time.Second
	dialed, err := net.DialTimeout(network, s.cfg.Local.Forward, timeout)
	if err != nil {
		slog.Error(err)
		return
	}
	slog.Verbosef("server foward: %v -> %v", accepted.RemoteAddr(), dialed.RemoteAddr())
	s.f.Forward(accepted, dialed)
}

func (s *Service) serveLocal(l net.Listener) {
	for {
		accepted, err := l.Accept()
		if err != nil {
			slog.Error(err)
			return
		}
		ss := s.findSession()
		if ss == nil {
			_ = accepted.Close()
			s.notifyRedial()
			continue
		}
		go s.dialStream(ss, accepted)
	}
}

func (s *Service) findSession() *session.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ss := range s.sessions {
		return ss
	}
	return nil
}

func (s *Service) dialStream(ss *session.Session, accepted net.Conn) {
	dialed, err := ss.Open()
	if err != nil {
		slog.Error(err)
		s.deleteSession(ss)
		return
	}
	slog.Verbosef("local foward: %v -> %v", accepted.RemoteAddr(), ss.Addr())
	s.f.Forward(accepted, dialed)
}

func (s *Service) addContext(ctx context.Context, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts[ctx] = cancel
}

func (s *Service) deleteContext(ctx context.Context) {
	var cancel context.CancelFunc
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel = s.contexts[ctx]
	delete(s.contexts, ctx)
}

func (s *Service) dialTLS(address string) {
	begin := time.Now()
	timeout := time.Duration(s.cfg.Server.DialTimeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.addContext(ctx, cancel)
	defer s.deleteContext(ctx)
	ss, err := session.DialContext(ctx, address, s.clientCfg)
	if err != nil {
		slog.Error(err)
		return
	}
	slog.Infof("new session to: %v, setup: %v", ss.Addr(), time.Since(begin))
	s.addSession(ss)
	go s.serveSession(ss)
}

func (s *Service) redialWait(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.shutdownSig:
	}
}

func (s *Service) redial() {
	addresses := s.cfg.Server.Dial
	if len(addresses) == 0 {
		return
	}
	for range s.redialSig {
		select {
		case <-s.shutdownSig:
			return
		default:
		}
		for s.numSessions() < 1 {
			addr := addresses[s.current]
			s.current = (s.current + 1) % len(addresses)
			s.dialTLS(addr)
			s.redialWait(2 * time.Second)
		}
	}
}

func (s *Service) serveTLS(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			slog.Error(err)
			return
		}
		go s.serveOneTLS(conn)
	}
}

func (s *Service) serveOneTLS(conn net.Conn) {
	atomic.AddUint32(&s.unauthorized, 1)
	defer atomic.AddUint32(&s.unauthorized, ^uint32(0))
	begin := time.Now()
	timeout := time.Duration(s.cfg.Server.AuthTimeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.addContext(ctx, cancel)
	defer s.deleteContext(ctx)
	ss, err := session.ServeContext(ctx, conn, s.serverCfg)
	if err != nil {
		slog.Error(err)
		return
	}
	slog.Infof("new session from: %v, setup: %v", ss.Addr(), time.Since(begin))
	s.addSession(ss)
	go s.serveSession(ss)
}

func (s *Service) Start() error {
	if s.cfg.Server.Listen != "" {
		l, err := net.Listen(network, s.cfg.Server.Listen)
		if err != nil {
			return err
		}
		s.tlsListener = l
		hcfg := s.cfg.NewHardenConfig(func() uint32 {
			return atomic.LoadUint32(&s.unauthorized)
		})
		go s.serveTLS(hlistener.Wrap(l, hcfg))
	}
	if s.cfg.Local.Listen != "" {
		l, err := net.Listen(network, s.cfg.Local.Listen)
		if err != nil {
			return err
		}
		s.tcpListener = l
		go s.serveLocal(s.tcpListener)
	}
	go s.redial()
	s.notifyRedial()
	return nil
}

func (s *Service) Shutdown() {
	close(s.shutdownSig)
	if s.tlsListener != nil {
		_ = s.tlsListener.Close()
	}
	if s.tcpListener != nil {
		_ = s.tcpListener.Close()
	}
	s.f.Close()
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for ctx, cancel := range s.contexts {
			delete(s.contexts, ctx)
			cancel()
		}
	}()
}
