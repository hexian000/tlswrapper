// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"crypto/tls"
	"testing"
)

func TestConfigWindowOptions(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantDial   int
		wantServer int
	}{
		{name: "dynamic", cfg: Config{}, wantDial: 6, wantServer: 6},
		{name: "session-only", cfg: Config{SessionWindow: 256 * 1024}, wantDial: 7, wantServer: 7},
		{name: "stream-only", cfg: Config{StreamWindow: 256 * 1024}, wantDial: 7, wantServer: 7},
		{name: "both", cfg: Config{SessionWindow: 256 * 1024, StreamWindow: 512 * 1024}, wantDial: 8, wantServer: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(tt.cfg.grpcDialOptions()); got != tt.wantDial {
				t.Fatalf("grpcDialOptions() = %d, want %d", got, tt.wantDial)
			}
			if got := len(tt.cfg.grpcServerOptions()); got != tt.wantServer {
				t.Fatalf("grpcServerOptions() = %d, want %d", got, tt.wantServer)
			}
		})
	}
}

func TestConfigALPN(t *testing.T) {
	if got := (&Config{}).alpn(); got != defaultH2ALPN {
		t.Fatalf("default alpn() = %q, want %q", got, defaultH2ALPN)
	}
	if got := (&Config{ALPN: "myproto"}).alpn(); got != "myproto" {
		t.Fatalf("alpn() = %q, want %q", got, "myproto")
	}
}

func TestConfigAppliedTLSConfig(t *testing.T) {
	// Plaintext mode: no TLS config resolves to nil.
	if got := (&Config{}).appliedTLSConfig(); got != nil {
		t.Fatalf("appliedTLSConfig() with no TLS = %v, want nil", got)
	}

	base := &tls.Config{MinVersion: tls.VersionTLS13}
	cfg := &Config{TLSConfig: base, ServerName: "example.com", ALPN: "myproto"}
	applied := cfg.appliedTLSConfig()
	if applied == nil {
		t.Fatal("appliedTLSConfig() = nil, want non-nil")
	}
	// The original config must not be mutated (Clone semantics).
	if base.ServerName != "" || len(base.NextProtos) != 0 {
		t.Fatal("appliedTLSConfig mutated the source TLS config")
	}
	if applied.ServerName != "example.com" {
		t.Fatalf("ServerName = %q, want %q", applied.ServerName, "example.com")
	}
	if len(applied.NextProtos) != 1 || applied.NextProtos[0] != "myproto" {
		t.Fatalf("NextProtos = %v, want [myproto]", applied.NextProtos)
	}

	// Without an explicit ALPN, the default identifier is advertised.
	applied = (&Config{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}).appliedTLSConfig()
	if len(applied.NextProtos) != 1 || applied.NextProtos[0] != defaultH2ALPN {
		t.Fatalf("default NextProtos = %v, want [%s]", applied.NextProtos, defaultH2ALPN)
	}

	// TLSConfigProvider takes precedence over TLSConfig.
	provided := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "ignored"}
	pcfg := &Config{
		TLSConfig:         &tls.Config{ServerName: "fromfield"},
		TLSConfigProvider: func() *tls.Config { return provided },
	}
	if got := pcfg.appliedTLSConfig(); got.ServerName != "ignored" {
		t.Fatalf("provider precedence: ServerName = %q, want %q", got.ServerName, "ignored")
	}
}

func TestConfigIgnoresTooSmallWindows(t *testing.T) {
	cfg := &Config{SessionWindow: 32 * 1024, StreamWindow: 48 * 1024}
	if got := cfg.sessionWindow(); got != 0 {
		t.Fatalf("sessionWindow() = %d, want 0", got)
	}
	if got := cfg.streamWindow(); got != 0 {
		t.Fatalf("streamWindow() = %d, want 0", got)
	}
}
