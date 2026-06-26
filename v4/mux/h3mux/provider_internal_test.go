// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"crypto/tls"
	"slices"
	"sync"
	"testing"
)

// TestTLSServerConfigProviderRotation verifies that tlsServerConfig resolves
// TLSConfigProvider on every inbound handshake via GetConfigForClient, so
// certificate rotation takes effect without restarting the listener.
func TestTLSServerConfigProviderRotation(t *testing.T) {
	var mu sync.Mutex
	current := &tls.Config{ServerName: "gen-1"}
	cfg := &Config{
		TLSConfigProvider: func() *tls.Config {
			mu.Lock()
			defer mu.Unlock()
			return current
		},
	}

	sc := cfg.tlsServerConfig()
	if !slices.Contains(sc.NextProtos, alpn) {
		t.Fatalf("NextProtos = %v, want to contain %q", sc.NextProtos, alpn)
	}
	if sc.GetConfigForClient == nil {
		t.Fatal("GetConfigForClient = nil, want per-handshake resolver")
	}

	got, err := sc.GetConfigForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerName != "gen-1" {
		t.Fatalf("ServerName = %q, want %q", got.ServerName, "gen-1")
	}

	// Rotate: the next inbound handshake must observe the new config.
	mu.Lock()
	current = &tls.Config{ServerName: "gen-2"}
	mu.Unlock()
	got, err = sc.GetConfigForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerName != "gen-2" {
		t.Fatalf("ServerName after rotation = %q, want %q", got.ServerName, "gen-2")
	}
	if !slices.Contains(got.NextProtos, alpn) {
		t.Fatalf("rotated NextProtos = %v, want to contain %q", got.NextProtos, alpn)
	}

	// A nil provider result must fail the handshake instead of panicking.
	mu.Lock()
	current = nil
	mu.Unlock()
	if _, err := sc.GetConfigForClient(nil); err == nil {
		t.Fatal("GetConfigForClient with nil provider result = nil error, want error")
	}
}
