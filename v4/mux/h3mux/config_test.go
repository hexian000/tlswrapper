// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"crypto/tls"
	"testing"
	"time"
)

func TestKeepAlivePeriodDefault(t *testing.T) {
	cfg := &Config{}
	if got := cfg.keepAlivePeriod(); got != 25*time.Second {
		t.Fatalf("keepAlivePeriod() = %v, want 25s", got)
	}
}

func TestKeepAlivePeriodOverride(t *testing.T) {
	cfg := &Config{KeepAlivePeriod: 10 * time.Second}
	if got := cfg.keepAlivePeriod(); got != 10*time.Second {
		t.Fatalf("keepAlivePeriod() = %v, want 10s", got)
	}
}

func TestMaxIncomingStreamsDefault(t *testing.T) {
	cfg := &Config{}
	if got := cfg.maxIncomingStreams(); got != 1024 {
		t.Fatalf("maxIncomingStreams() = %d, want 1024", got)
	}
}

func TestMaxIncomingStreamsOverride(t *testing.T) {
	cfg := &Config{MaxIncomingStreams: 512}
	if got := cfg.maxIncomingStreams(); got != 512 {
		t.Fatalf("maxIncomingStreams() = %d, want 512", got)
	}
}

// TestQuicConfigOptionalFields verifies that non-zero optional Config fields
// are propagated into the resulting *quic.Config.
func TestQuicConfigOptionalFields(t *testing.T) {
	cfg := &Config{
		HandshakeTimeout:               5 * time.Second,
		MaxIdleTimeout:                 30 * time.Second,
		InitialStreamReceiveWindow:     1 << 20,
		MaxStreamReceiveWindow:         2 << 20,
		InitialConnectionReceiveWindow: 4 << 20,
		MaxConnectionReceiveWindow:     8 << 20,
	}
	qcfg := cfg.quicConfig()
	if qcfg.HandshakeIdleTimeout == 0 {
		t.Error("HandshakeIdleTimeout not set")
	}
	if qcfg.MaxIdleTimeout == 0 {
		t.Error("MaxIdleTimeout not set")
	}
	if qcfg.InitialStreamReceiveWindow == 0 {
		t.Error("InitialStreamReceiveWindow not set")
	}
	if qcfg.MaxStreamReceiveWindow == 0 {
		t.Error("MaxStreamReceiveWindow not set")
	}
	if qcfg.InitialConnectionReceiveWindow == 0 {
		t.Error("InitialConnectionReceiveWindow not set")
	}
	if qcfg.MaxConnectionReceiveWindow == 0 {
		t.Error("MaxConnectionReceiveWindow not set")
	}
}

// TestPrependALPNNoDuplicate verifies that if alpn is already in NextProtos
// it is not inserted again.
func TestPrependALPNNoDuplicate(t *testing.T) {
	cfg := &Config{
		TLSConfig: &tls.Config{
			NextProtos: []string{alpn, "other"},
		},
	}
	result := cfg.tlsClientConfig()
	count := 0
	for _, proto := range result.NextProtos {
		if proto == alpn {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("alpn %q appears %d times in NextProtos, want 1: %v", alpn, count, result.NextProtos)
	}
}
