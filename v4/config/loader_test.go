// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMaxStartups(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantStart int
		wantRate  int
		wantFull  int
		wantErr   bool
	}{
		{name: "valid", input: "10:30:60", wantStart: 10, wantRate: 30, wantFull: 60},
		{name: "zero-rate", input: "5:0:100", wantStart: 5, wantRate: 0, wantFull: 100},
		{name: "full-equals-start", input: "10:50:10", wantStart: 10, wantRate: 50, wantFull: 10},
		{name: "missing-parts", input: "10:30", wantErr: true},
		{name: "no-colons", input: "invalid", wantErr: true},
		{name: "start-zero", input: "0:30:60", wantErr: true},
		{name: "rate-negative", input: "10:-1:60", wantErr: true},
		{name: "rate-over-100", input: "10:101:60", wantErr: true},
		{name: "full-less-than-start", input: "10:30:5", wantErr: true},
		{name: "non-numeric", input: "a:b:c", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, rate, full, err := parseMaxStartups(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMaxStartups(%q) = nil error, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMaxStartups(%q) error: %v", tt.input, err)
			}
			if start != tt.wantStart || rate != tt.wantRate || full != tt.wantFull {
				t.Fatalf("got (%d,%d,%d), want (%d,%d,%d)", start, rate, full, tt.wantStart, tt.wantRate, tt.wantFull)
			}
		})
	}
}

func TestCheckType(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid", input: Type},
		{name: "wrong-mediatype", input: "text/plain", wantErr: true},
		{name: "missing-version", input: "application/x-tlswrapper-config", wantErr: true},
		{name: "wrong-version", input: "application/x-tlswrapper-config; version=3", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("checkType(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkType(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Run("clamps-negative-limits", func(t *testing.T) {
		c := Default
		c.MaxSessions = -1
		c.Mux.MaxStreams = -1
		c.Mux.MaxHalfOpen = -1
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.MaxSessions != 0 {
			t.Fatalf("MaxSessions = %d, want 0", c.MaxSessions)
		}
		if c.Mux.MaxStreams != 0 {
			t.Fatalf("Mux.MaxStreams = %d, want 0", c.Mux.MaxStreams)
		}
		if c.Mux.MaxHalfOpen != 0 {
			t.Fatalf("Mux.MaxHalfOpen = %d, want 0", c.Mux.MaxHalfOpen)
		}
	})

	t.Run("clamps-ping-timeout-low", func(t *testing.T) {
		c := Default
		c.Mux.PingTimeout = 1
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.PingTimeout != 10 {
			t.Fatalf("PingTimeout = %d, want 10", c.Mux.PingTimeout)
		}
	})

	t.Run("preserves-keepalive-above-ping-timeout", func(t *testing.T) {
		c := Default
		c.Mux.PingTimeout = 10
		c.Mux.KeepAlive = 20
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.KeepAlive != 20 {
			t.Fatalf("KeepAlive = %d, want %d", c.Mux.KeepAlive, 20)
		}
	})

	t.Run("clamps-send-timeout-low", func(t *testing.T) {
		c := Default
		c.Mux.SendTimeout = 1
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.SendTimeout != 10 {
			t.Fatalf("SendTimeout = %d, want 10", c.Mux.SendTimeout)
		}
	})

	t.Run("clamps-connect-timeout-low", func(t *testing.T) {
		c := Default
		c.Mux.ConnectTimeout = 0
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.ConnectTimeout != 10 {
			t.Fatalf("ConnectTimeout = %d, want 10", c.Mux.ConnectTimeout)
		}
	})

	t.Run("rejects-invalid-max-startups", func(t *testing.T) {
		c := Default
		c.MaxStartups = "bad"
		if err := c.Validate(); err == nil {
			t.Fatal("expected error for invalid MaxStartups")
		}
	})

	t.Run("clamps-session-window-too-small", func(t *testing.T) {
		c := Default
		c.Mux.SessionWindow = 1024 // non-zero but below minimum 65535
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.SessionWindow != 65535 {
			t.Fatalf("SessionWindow = %d, want 65535", c.Mux.SessionWindow)
		}
	})

	t.Run("clamps-stream-window-too-small", func(t *testing.T) {
		c := Default
		c.Mux.StreamWindow = 1024
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.StreamWindow != 65535 {
			t.Fatalf("StreamWindow = %d, want 65535", c.Mux.StreamWindow)
		}
	})

	t.Run("clamps-mux-backlog-low", func(t *testing.T) {
		c := Default
		c.Mux.TCP.Backlog = 0
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.TCP.Backlog != 1 {
			t.Fatalf("Mux.TCP.Backlog = %d, want 1", c.Mux.TCP.Backlog)
		}
	})

	t.Run("clamps-socket-buffers-low", func(t *testing.T) {
		c := Default
		c.Mux.TCP.ReadBuffer = -1
		c.TCP.WriteBuffer = -1
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Mux.TCP.ReadBuffer != 1 {
			t.Fatalf("Mux.TCP.RcvBuf = %d, want 1", c.Mux.TCP.ReadBuffer)
		}
		if c.TCP.WriteBuffer != 1 {
			t.Fatalf("TCP.SndBuf = %d, want 1", c.TCP.WriteBuffer)
		}
	})

	t.Run("rejects-wrong-type", func(t *testing.T) {
		c := Default
		c.Type = "text/plain"
		if err := c.Validate(); err == nil {
			t.Fatal("expected error for wrong type")
		}
	})

	t.Run("h3mux-without-tls-fails", func(t *testing.T) {
		c := Default
		c.MuxProtocol = "h3mux"
		// TLS is nil — must be rejected
		if err := c.Validate(); err == nil {
			t.Fatal("expected error: h3mux without TLS should be rejected")
		}
	})

	t.Run("h3mux-with-tls-passes", func(t *testing.T) {
		c := Default
		c.MuxProtocol = "h3mux"
		c.TLS = &TLS{} // non-nil pointer is all Validate checks
		if err := c.Validate(); err != nil {
			t.Fatalf("unexpected error for h3mux with TLS: %v", err)
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("valid-minimal", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{"type": Type})
		cfg, err := Load(data)
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil {
			t.Fatal("Load returned nil config")
		}
	})

	t.Run("invalid-json", func(t *testing.T) {
		_, err := Load([]byte("{not json}"))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("wrong-version", func(t *testing.T) {
		data := []byte(`{"type":"application/x-tlswrapper-config; version=99"}`)
		_, err := Load(data)
		if err == nil {
			t.Fatal("expected error for wrong version")
		}
	})

	t.Run("invalid-max-startups", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"type":         Type,
			"max_startups": "bad:format",
		})
		_, err := Load(data)
		if err == nil {
			t.Fatal("expected error for invalid max_startups")
		}
	})

	t.Run("overrides-defaults", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"type":       Type,
			"api_listen": "127.0.0.1:9090",
			"mux": map[string]any{
				"timeout":      30,
				"keepalive":    10,
				"send_timeout": 12,
			},
		})
		cfg, err := Load(data)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mux.PingTimeout != 30 {
			t.Fatalf("PingTimeout = %d, want 30", cfg.Mux.PingTimeout)
		}
		if cfg.APIListen != "127.0.0.1:9090" {
			t.Fatalf("APIListen = %q, want %q", cfg.APIListen, "127.0.0.1:9090")
		}
	})

	t.Run("preserves-default-keepalive-on-round-trip", func(t *testing.T) {
		b, err := Default.Dump()
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(b)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mux.KeepAlive != Default.Mux.KeepAlive {
			t.Fatalf("KeepAlive = %d, want %d", cfg.Mux.KeepAlive, Default.Mux.KeepAlive)
		}
	})

	t.Run("tls-inline-success", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"type": Type,
			"tls": map[string]any{
				"cert":      utilsTestCertPEM,
				"key":       utilsTestKeyPEM,
				"authcerts": []string{utilsTestCertPEM},
			},
		})
		cfg, err := Load(data)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TLS == nil {
			t.Fatal("TLS should not be nil")
		}
		if len(cfg.TLS.AuthCerts) != 1 {
			t.Fatalf("len(AuthCerts) = %d, want 1", len(cfg.TLS.AuthCerts))
		}
	})

	t.Run("loads-socket-buffers", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"type": Type,
			"mux": map[string]any{
				"tcp": map[string]any{
					"rcvbuf": 4096,
					"sndbuf": 8192,
				},
			},
			"tcp": map[string]any{
				"rcvbuf": 16384,
				"sndbuf": 32768,
			},
		})
		cfg, err := Load(data)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mux.TCP.ReadBuffer != 4096 || cfg.Mux.TCP.WriteBuffer != 8192 {
			t.Fatalf("Mux.TCP buffers = (%d,%d), want (4096,8192)", cfg.Mux.TCP.ReadBuffer, cfg.Mux.TCP.WriteBuffer)
		}
		if cfg.TCP.ReadBuffer != 16384 || cfg.TCP.WriteBuffer != 32768 {
			t.Fatalf("TCP buffers = (%d,%d), want (16384,32768)", cfg.TCP.ReadBuffer, cfg.TCP.WriteBuffer)
		}
	})
}

func TestParsedMaxStartups(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		c := Default
		c.MaxStartups = ""
		s, r, f := c.ParsedMaxStartups()
		if s != 0 || r != 0 || f != 0 {
			t.Fatalf("expected (0,0,0), got (%d,%d,%d)", s, r, f)
		}
	})

	t.Run("valid", func(t *testing.T) {
		c := Default
		c.MaxStartups = "5:50:20"
		s, r, f := c.ParsedMaxStartups()
		if s != 5 || r != 50 || f != 20 {
			t.Fatalf("expected (5,50,20), got (%d,%d,%d)", s, r, f)
		}
	})
}

func TestClone(t *testing.T) {
	cfg := Default
	cfg.APIListen = "127.0.0.1:8080"
	cfg.Identity.Claim = "original"
	clone, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	// Modify original; clone should be unaffected.
	cfg.Identity.Claim = "modified"
	if clone.Identity.Claim != "original" {
		t.Fatalf("clone.Identity.Claim = %q, want %q", clone.Identity.Claim, "original")
	}
	if clone.APIListen != "127.0.0.1:8080" {
		t.Fatalf("clone.APIListen = %q, want %q", clone.APIListen, "127.0.0.1:8080")
	}
}

func TestDump(t *testing.T) {
	cfg := Default
	b, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"type"`) {
		t.Fatal("dump output missing 'type' field")
	}
	// Verify the output is valid indented JSON.
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("dump output is not valid JSON: %v", err)
	}
}

func TestFindListen(t *testing.T) {
	cfg := &File{
		Listen:     "127.0.0.1:8000",
		MuxConnect: "remote:7000",
		Connect:    "backend:9000",
		Identity: Identity{
			Claim:      "self",
			MuxConnect: []string{"peer-a:7001"},
			Listen: map[string]string{
				"peer-a": "127.0.0.1:8001",
			},
		},
	}

	t.Run("default-entry", func(t *testing.T) {
		if got := cfg.FindListen(""); got != "127.0.0.1:8000" {
			t.Fatalf("FindListen(%q) = %q, want %q", "", got, "127.0.0.1:8000")
		}
	})

	t.Run("named-peer-listen", func(t *testing.T) {
		if got := cfg.FindListen("peer-a"); got != "127.0.0.1:8001" {
			t.Fatalf("FindListen(%q) = %q, want %q", "peer-a", got, "127.0.0.1:8001")
		}
	})

	t.Run("unknown-peer", func(t *testing.T) {
		if got := cfg.FindListen("unknown"); got != "" {
			t.Fatalf("FindListen(%q) = %q, want empty", "unknown", got)
		}
	})
}

func TestLoadPEM(t *testing.T) {
	t.Run("plain-string", func(t *testing.T) {
		got, err := loadPEM("INLINE")
		if err != nil {
			t.Fatal(err)
		}
		if got != "INLINE" {
			t.Fatalf("loadPEM() = %q, want %q", got, "INLINE")
		}
	})

	t.Run("load-from-file", func(t *testing.T) {
		dir := t.TempDir()
		pemPath := filepath.Join(dir, "cert.pem")
		want := "CERT-DATA"
		if err := os.WriteFile(pemPath, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadPEM("@" + pemPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("loadPEM() = %q, want %q", got, want)
		}
	})

	t.Run("load-file-error", func(t *testing.T) {
		_, err := loadPEM("@/path/does/not/exist")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestLoadFile(t *testing.T) {
	t.Run("valid-file", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.json")
		data, _ := json.Marshal(map[string]any{"type": Type})
		if err := os.WriteFile(configPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil {
			t.Fatal("LoadFile returned nil config")
		}
	})

	t.Run("read-error", func(t *testing.T) {
		_, err := LoadFile("/path/does/not/exist")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("tls-file-reference-error", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"type": Type,
			"tls": map[string]any{
				"cert": "@/path/does/not/exist",
				"key":  "INLINE",
			},
		})
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.json")
		if err := os.WriteFile(configPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadFile(configPath)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
