// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/hexian000/tlswrapper/v4/mux"
	"github.com/hexian000/tlswrapper/v4/mux/h2mux"
)

func lossymuxTLS(t *testing.T) *config.TLS {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"example.com"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return &config.TLS{Certificate: cert, PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})), AuthCerts: []string{cert}}
}

// The same target address handles TCP and UDP, exercising connect's dual use.
func lossymuxEcho(t *testing.T, host string) string {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	addr, err := net.ResolveUDPAddr("udp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	u, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	go func() {
		buf := make([]byte, 65536)
		for {
			n, client, err := u.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			_, _ = u.WriteToUDPAddrPort(buf[:n], client)
		}
	}()
	return l.Addr().String()
}

func startLossymuxServer(t *testing.T, cfg config.File) *Server {
	t.Helper()
	cfg.Log = "discard"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}

func lossymuxPair(t *testing.T, protocol, host string, identity bool) (*Server, *Server, *tunnel, string) {
	t.Helper()
	cfg := config.Default
	cfg.MuxProtocol = protocol
	cfg.Mux.LossyMux.Enabled = true
	cfg.MuxListen = net.JoinHostPort(host, "0")
	cfg.Connect = lossymuxEcho(t, host)
	cfg.Identity.Claim = "remote"
	if protocol == "h3mux" {
		cfg.TLS = lossymuxTLS(t)
	}
	s := startLossymuxServer(t, cfg)
	cfg.MuxListen = ""
	cfg.NoRedial = true
	cfg.Identity = config.Identity{Claim: "local"}
	if identity {
		cfg.Identity.MuxConnect = []string{s.muxListener.Addr().String()}
		cfg.Identity.Listen = map[string]string{"remote": net.JoinHostPort(host, "0")}
	} else {
		cfg.MuxConnect = s.muxListener.Addr().String()
		cfg.Listen = net.JoinHostPort(host, "0")
	}
	c := startLossymuxServer(t, cfg)
	tn := c.mainTunnel
	if identity {
		tn = c.identityTunnels[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := tn.dial(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { s.udp.mu.Lock(); defer s.udp.mu.Unlock(); return len(s.udp.sessions) == 1 })
	addr := ""
	if identity {
		addr = c.identities["remote"].l.Addr().String()
	} else {
		addr = c.localListener.Addr().String()
	}
	return c, s, tn, addr
}

func lossymuxClient(t *testing.T, addr string) *net.UDPConn {
	t.Helper()
	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialUDP("udp", nil, uaddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func lossymuxRoundTrip(t *testing.T, c *net.UDPConn, p []byte) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(p); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65536)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], p) {
		t.Fatalf("UDP data changed: %d != %d bytes", n, len(p))
	}
}

func TestLossyMuxUDPForwarding(t *testing.T) {
	for _, protocol := range []string{"h2mux", "h3mux"} {
		for _, route := range []string{"default", "identity", "ipv6"} {
			t.Run(protocol+"/"+route, func(t *testing.T) {
				host := "127.0.0.1"
				if route == "ipv6" {
					host = "::1"
				}
				c, s, tn, addr := lossymuxPair(t, protocol, host, route == "identity")
				one, two := lossymuxClient(t, addr), lossymuxClient(t, addr)
				lossymuxRoundTrip(t, one, nil)
				lossymuxRoundTrip(t, one, []byte("same flow"))
				lossymuxRoundTrip(t, two, []byte("second client"))
				if protocol == "h2mux" {
					lossymuxRoundTrip(t, one, bytes.Repeat([]byte("z"), 60000))
				}
				if got := c.Stats().NumUDPFlows; got != 2 {
					t.Fatalf("flow pinning: %d flows", got)
				}
				if got := s.Stats().NumUDPFlows; got != 2 {
					t.Fatalf("passive flows: %d", got)
				}
				conn, err := net.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				_, err = conn.Write([]byte("TCP"))
				if err != nil {
					t.Fatal(err)
				}
				b := make([]byte, 3)
				if _, err = io.ReadFull(conn, b); err != nil || string(b) != "TCP" {
					t.Fatalf("TCP: %q %v", b, err)
				}
				_ = conn.Close()
				waitFor(t, time.Second, func() bool { return c.Stats().DatagramsSent >= 3 && s.Stats().DatagramsReceived >= 3 })
				r := httptest.NewRecorder()
				newAPIMetricsHandler(c).ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
				for _, metric := range []string{"tlswrapper_datagrams_total", "tlswrapper_datagram_bytes_total", "tlswrapper_datagrams_dropped_total", "tlswrapper_udp_flows 2", "tlswrapper_session_udp_flows{identity=\"remote\"} 2"} {
					if !strings.Contains(r.Body.String(), metric) {
						t.Fatalf("missing %s: %s", metric, r.Body.String())
					}
				}
				before := c.Stats().DatagramsSent
				ss := tn.getSession()
				_ = ss.Close()
				waitFor(t, 3*time.Second, func() bool {
					return c.Stats().NumSessionsFinalized == 1 && c.Stats().NumUDPFlows == 0 && s.Stats().NumUDPFlows == 0
				})
				if c.Stats().DatagramsSent < before {
					t.Fatal("counters lost on reconnect")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, err := tn.dial(ctx); err != nil {
					t.Fatal(err)
				}
				waitFor(t, time.Second, func() bool { s.udp.mu.Lock(); defer s.udp.mu.Unlock(); return len(s.udp.sessions) == 1 })
				lossymuxRoundTrip(t, one, []byte("fresh session"))
			})
		}
	}
}

func TestLossyMuxReloadAndExpiry(t *testing.T) {
	for _, protocol := range []string{"h2mux", "h3mux"} {
		t.Run(protocol, func(t *testing.T) {
			c, s, tn, addr := lossymuxPair(t, protocol, "127.0.0.1", false)
			client := lossymuxClient(t, addr)
			lossymuxRoundTrip(t, client, []byte("keep"))
			ss := tn.getSession()
			muxListener := s.muxListener
			cfg, _ := c.getConfig()
			next, err := cfg.Clone()
			if err != nil {
				t.Fatal(err)
			}
			next.Mux.LossyMux.Queue = 2
			next.Mux.LossyMux.IdleTimeout = 1
			if err := c.ReloadConfig(next); err != nil {
				t.Fatal(err)
			}
			if tn.getSession() != ss {
				t.Fatal("reload killed active UDP session")
			}
			lossymuxRoundTrip(t, client, []byte("live limits"))
			cfg, _ = s.getConfig()
			remote, err := cfg.Clone()
			if err != nil {
				t.Fatal(err)
			}
			remote.Mux.LossyMux.Queue = 2
			if err := s.ReloadConfig(remote); err != nil {
				t.Fatal(err)
			}
			if s.muxListener != muxListener || tn.getSession() != ss {
				t.Fatal("extension-only reload replaced transport")
			}
			// Disabling the local UDP listener retains its socket for existing replies.
			disabled, err := next.Clone()
			if err != nil {
				t.Fatal(err)
			}
			disabled.Mux.LossyMux.Enabled = false
			if err := c.ReloadConfig(disabled); err != nil {
				t.Fatal(err)
			}
			c.udp.mu.Lock()
			u := c.udp.sessions[ss]
			var flow *udpFlow
			for _, f := range u.flows {
				flow = f
			}
			listeners := len(c.udp.listeners)
			c.udp.mu.Unlock()
			if listeners != 0 || flow == nil {
				t.Fatal("disable did not retain draining flow")
			}
			c.udp.receive(u, mux.Datagram{FlowID: flow.id, Data: []byte("draining reply")})
			_ = client.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 64)
			n, err := client.Read(buf)
			if err != nil || string(buf[:n]) != "draining reply" {
				t.Fatalf("retired socket: %q %v", buf[:n], err)
			}
			if err := c.ReloadConfig(next); err != nil {
				t.Fatal(err)
			}
			lossymuxRoundTrip(t, client, []byte("reenabled"))
			if c.Stats().NumUDPFlows != 1 {
				t.Fatal("reenable duplicated pinned flow")
			}
			c.udp.mu.Lock()
			for _, f := range u.flows {
				f.lastSeen = time.Now().Add(-2 * time.Second)
			}
			c.udp.mu.Unlock()
			c.udp.sweep(time.Now())
			waitFor(t, time.Second, func() bool { return c.Stats().NumUDPFlows == 0 && s.Stats().NumUDPFlows == 0 })
		})
	}
}

func TestLossyMuxListenerDisabledAndBindFailure(t *testing.T) {
	s := startLossymuxServer(t, config.File{Type: config.Type, Listen: "127.0.0.1:0", Mux: config.Mux{LossyMux: config.LossyMux{Queue: 64, IdleTimeout: 60}}})
	uaddr, _ := net.ResolveUDPAddr("udp", s.localListener.Addr().String())
	occupied, err := net.ListenUDP("udp", uaddr)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg, _ := s.getConfig()
	next, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	next.Mux.LossyMux.Enabled = true
	if err := s.ReloadConfig(next); err == nil {
		t.Fatal("UDP bind failure not reported")
	}
	_ = occupied.Close()
	if err := s.ReloadConfig(next); err != nil {
		t.Fatal("identical failed reload was not retryable:", err)
	}
	// Startup has the same failure contract and leaves cleanup to Shutdown.
	cfg2 := config.Default
	cfg2.Listen = s.localListener.Addr().String()
	cfg2.Mux.LossyMux.Enabled = true
	// A separate TCP socket establishes an address whose UDP port is occupied.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	uaddr, _ = net.ResolveUDPAddr("udp", l.Addr().String())
	occupied2, err := net.ListenUDP("udp", uaddr)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied2.Close()
	cfg2.Listen = l.Addr().String()
	_ = l.Close()
	s2, err := NewServer(&cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Shutdown()
	if err := s2.Start(); err == nil {
		t.Fatal("startup accepted unavailable UDP port")
	}
}

func TestLossyMuxUnknownFlowsAndIDExhaustion(t *testing.T) {
	cli, srv := newMuxSessionPair(t, &h2mux.Config{LossyMux: true}, &h2mux.Config{LossyMux: true})
	s := newTestServer(t, nil)
	defer s.Shutdown()
	s.udp.attach(srv)
	d := cli.(mux.DatagramSession).Datagrams()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, id := range []uint16{1, 2} {
		_ = d.TrySend(id, nil)
		v, err := d.Receive(ctx)
		if err != nil || !v.Close || v.FlowID != id {
			t.Fatalf("refused flow: %+v %v", v, err)
		}
	}
	s.udp.mu.Lock()
	u := s.udp.sessions[srv]
	s.udp.mu.Unlock()
	s.udp.receive(u, mux.Datagram{FlowID: 999, Close: true})
	if srv.Stats().DatagramsDropped.Load() != 2 || srv.Stats().NumUDPFlows.Load() != 0 {
		t.Fatal("unknown close changed accounting")
	}
	for _, client := range []bool{false, true} {
		q := mux.NewDatagramChannel(true, client, 1, nil)
		u := &udpSession{d: q, flows: make(map[uint16]*udpFlow), next: 2 - q.LocalParity()}
		for id := uint32(u.next); id <= 65535; id += 2 {
			u.flows[uint16(id)] = &udpFlow{}
		}
		if id := s.udp.allocIDLocked(u); id != 0 {
			t.Fatalf("exhausted allocated %d", id)
		}
		free := 2 - q.LocalParity()
		delete(u.flows, free)
		if id := s.udp.allocIDLocked(u); id != free {
			t.Fatalf("did not reuse %d: %d", free, id)
		}
		q.Close()
	}
}

func TestLossyMuxDemandRedialDropsTrigger(t *testing.T) {
	for _, protocol := range []string{"h2mux", "h3mux"} {
		t.Run(protocol, func(t *testing.T) {
			c, s, tn, addr := lossymuxPair(t, protocol, "127.0.0.1", false)
			client := lossymuxClient(t, addr)
			lossymuxRoundTrip(t, client, []byte("before disconnect"))
			_ = tn.getSession().Close()
			waitFor(t, time.Second, func() bool { return c.Stats().NumSessionsFinalized == 1 && s.Stats().NumSessionsFinalized == 1 })
			before := c.Stats()
			if _, err := client.Write([]byte("drop this trigger")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 3*time.Second, func() bool {
				s.udp.mu.Lock()
				defer s.udp.mu.Unlock()
				return tn.getSession() != nil && len(s.udp.sessions) == 1
			})
			if c.Stats().DatagramsSent != before.DatagramsSent || c.Stats().DatagramsDropped != before.DatagramsDropped+1 {
				t.Fatal("redial retained the triggering packet")
			}
			lossymuxRoundTrip(t, client, []byte("after demand redial"))
		})
	}
}

func TestLossyMuxReloadNegotiatesNewSessions(t *testing.T) {
	for _, protocol := range []string{"h2mux", "h3mux"} {
		t.Run(protocol, func(t *testing.T) {
			c, s, tn, addr := lossymuxPair(t, protocol, "127.0.0.1", false)
			client := lossymuxClient(t, addr)
			lossymuxRoundTrip(t, client, []byte("old offer"))
			old := tn.getSession()
			cfg, _ := s.getConfig()
			disabled, err := cfg.Clone()
			if err != nil {
				t.Fatal(err)
			}
			disabled.Mux.LossyMux.Enabled = false
			listener := s.muxListener
			if err := s.ReloadConfig(disabled); err != nil {
				t.Fatal(err)
			}
			if s.muxListener != listener || !old.(mux.DatagramSession).Datagrams().Ready() {
				t.Fatal("reload mutated established negotiation")
			}
			lossymuxRoundTrip(t, client, []byte("old session still negotiated"))
			_ = old.Close()
			waitFor(t, time.Second, func() bool { return c.Stats().NumSessionsFinalized == 1 && s.Stats().NumSessionsFinalized == 1 })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			fresh, err := tn.dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.(mux.DatagramSession).Datagrams() != nil {
				t.Fatal("disabled peer offered extension")
			}
			enabled, err := disabled.Clone()
			if err != nil {
				t.Fatal(err)
			}
			enabled.Mux.LossyMux.Enabled = true
			if err := s.ReloadConfig(enabled); err != nil {
				t.Fatal(err)
			}
			waitFor(t, time.Second, func() bool { return c.Stats().NumSessionsFinalized == 2 && s.Stats().NumSessionsFinalized == 2 })
			fresh, err = tn.dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !fresh.(mux.DatagramSession).Datagrams().Ready() {
				t.Fatal("new session did not negotiate live offer")
			}
			waitFor(t, time.Second, func() bool { s.udp.mu.Lock(); defer s.udp.mu.Unlock(); return len(s.udp.sessions) == 1 })
			lossymuxRoundTrip(t, client, []byte("new offer"))
		})
	}
}

func TestLossyMuxIdleActivityAndCounters(t *testing.T) {
	_, ss := newMuxSessionPair(t, &h2mux.Config{LossyMux: true}, &h2mux.Config{LossyMux: true})
	s := newTestServer(t, map[string]any{"mux": map[string]any{"idle_timeout": 10}})
	defer s.Shutdown()
	tn := newTunnel("", s)
	tn.ss = ss
	tn.stale = true
	ss.Stats().NumUDPFlows.Store(1)
	tn.checkIdle()
	if ss.IsClosed() {
		t.Fatal("stale eviction ignored active UDP flow")
	}
	tn.stale = false
	tn.idleSince = time.Now().Add(-20 * time.Second)
	ss.Stats().LastDatagram.Store(time.Now().UnixNano())
	tn.checkIdle()
	if ss.IsClosed() {
		t.Fatal("UDP activity did not refresh session idle timer")
	}
	ss.Stats().DatagramsSent.Store(7)
	s.trackSessionDatagrams(ss)
	_ = ss.Close()
	if got := s.Stats().DatagramsSent; got != 7 {
		t.Fatalf("counter reset during finalization: %d", got)
	}
	ss.Stats().NumUDPFlows.Store(0)
	s.flushSessionMetrics(ss)
	if got := s.Stats().DatagramsSent; got != 7 {
		t.Fatalf("counter changed on archive: %d", got)
	}
}

func TestLossyMuxRebindWaitsForSocketClose(t *testing.T) {
	cfg := config.Default
	cfg.Listen = "127.0.0.1:0"
	cfg.Mux.LossyMux.Enabled = true
	s := startLossymuxServer(t, cfg)
	r := s.udp
	r.mu.Lock()
	l := r.listeners[""]
	delete(r.listeners, "")
	l.active, l.refs = false, 0 // pause between releasing the last reference and Close
	r.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- r.syncListener("", l.addr) }()
	r.closeListener(l)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rebind stalled")
	}
	r.mu.Lock()
	replacement := r.listeners[""]
	r.mu.Unlock()
	if replacement == nil || replacement == l {
		t.Fatal("reactivated a closing socket")
	}
}
