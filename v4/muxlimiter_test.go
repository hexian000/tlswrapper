// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// fakeMuxSession implements mux.Session, tracking Close calls.
type fakeMuxSession struct {
	closed atomic.Bool
}

func (s *fakeMuxSession) Handshake(context.Context) error        { return nil }
func (s *fakeMuxSession) Open(context.Context) (net.Conn, error) { return nil, mux.ErrSessionClosed }
func (s *fakeMuxSession) Accept() (net.Conn, error)              { return nil, mux.ErrSessionClosed }
func (s *fakeMuxSession) Close() error                           { s.closed.Store(true); return nil }
func (s *fakeMuxSession) IsClosed() bool                         { return s.closed.Load() }
func (s *fakeMuxSession) CloseChan() <-chan struct{}             { return nil }
func (s *fakeMuxSession) IdleChan() <-chan struct{}              { return nil }
func (s *fakeMuxSession) Stats() *mux.SessionMetrics             { return nil }
func (s *fakeMuxSession) PeerIdentity() string                   { return "" }
func (s *fakeMuxSession) LocalAddr() net.Addr                    { return nil }
func (s *fakeMuxSession) RemoteAddr() net.Addr                   { return nil }

// fakeMuxListener returns the queued sessions in order, then ErrSessionClosed.
type fakeMuxListener struct {
	sessions []*fakeMuxSession
	next     int
}

func (l *fakeMuxListener) Accept() (mux.Session, error) {
	if l.next >= len(l.sessions) {
		return nil, mux.ErrSessionClosed
	}
	ss := l.sessions[l.next]
	l.next++
	return ss, nil
}
func (l *fakeMuxListener) Addr() net.Addr { return nil }
func (l *fakeMuxListener) Close() error   { return nil }

func TestHardenedMuxListenerMaxSessions(t *testing.T) {
	sessions := []*fakeMuxSession{{}, {}}
	numSessions := uint32(0)
	hml := newHardenedMuxListener(&fakeMuxListener{sessions: sessions}, hardenedMuxListenerConfig{
		MaxSessions: 1,
		Stats:       func() (uint32, uint32) { return numSessions, 0 },
	})

	// Under the limit: the first session is admitted.
	ss, err := hml.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if ss != mux.Session(sessions[0]) {
		t.Fatal("expected first session to be admitted")
	}
	// Over the limit: the second session is closed and Accept reports the
	// listener error from the drained fake.
	numSessions = 2
	if _, err := hml.Accept(); err == nil {
		t.Fatal("expected Accept to fail after limited session is dropped")
	}
	if !sessions[1].IsClosed() {
		t.Fatal("expected over-limit session to be closed")
	}
	accepted, served := hml.Stats()
	if accepted != 2 || served != 1 {
		t.Fatalf("Stats() = (%d, %d), want (2, 1)", accepted, served)
	}
}

func TestHardenedMuxListenerHalfOpenFull(t *testing.T) {
	sessions := []*fakeMuxSession{{}}
	hml := newHardenedMuxListener(&fakeMuxListener{sessions: sessions}, hardenedMuxListenerConfig{
		Start: 1,
		Full:  2,
		Rate:  1.0,
		Stats: func() (uint32, uint32) { return 0, 3 },
	})
	if _, err := hml.Accept(); err == nil {
		t.Fatal("expected Accept to fail after throttled session is dropped")
	}
	if !sessions[0].IsClosed() {
		t.Fatal("expected throttled session to be closed")
	}
}
