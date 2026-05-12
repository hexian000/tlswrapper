// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hexian000/gosnippets/slog"
)

// Test PEM material (self-signed, for testing only — copied from v4/tls_test.go).
const (
	utilsTestCertPEM = `-----BEGIN CERTIFICATE-----
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
tg==
-----END CERTIFICATE-----
`
	utilsTestKeyPEM = `-----BEGIN PRIVATE KEY-----
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
K8wzuiuQ06NyMgsYLd7aOEIZKMtMY8ko
-----END PRIVATE KEY-----
`
)

func TestNewTLSConfigNil(t *testing.T) {
	cfg := &File{}
	tlsCfg, err := cfg.NewTLSConfig("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		t.Fatal("expected nil tls.Config when TLS section is absent")
	}
}

func TestNewTLSConfigValid(t *testing.T) {
	cfg := &File{
		TLS: &TLS{
			Certificate: utilsTestCertPEM,
			PrivateKey:  utilsTestKeyPEM,
			AuthCerts:   []string{utilsTestCertPEM},
		},
	}
	tlsCfg, err := cfg.NewTLSConfig("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if tlsCfg.ServerName != "example.com" {
		t.Fatalf("ServerName = %q, want %q", tlsCfg.ServerName, "example.com")
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Fatalf("len(Certificates) = %d, want 1", len(tlsCfg.Certificates))
	}
}

func TestNewTLSConfigInvalidCert(t *testing.T) {
	cfg := &File{
		TLS: &TLS{
			Certificate: "not a certificate",
			PrivateKey:  "not a key",
		},
	}
	_, err := cfg.NewTLSConfig("example.com")
	if err == nil {
		t.Fatal("expected error for invalid certificate/key")
	}
}

func TestNewX509CertPool(t *testing.T) {
	t.Run("nil-input", func(t *testing.T) {
		pool, err := newX509CertPool(nil)
		if err != nil {
			t.Fatal(err)
		}
		if pool == nil {
			t.Fatal("expected non-nil cert pool for nil input")
		}
	})

	t.Run("valid-cert", func(t *testing.T) {
		pool, err := newX509CertPool([]string{utilsTestCertPEM})
		if err != nil {
			t.Fatal(err)
		}
		if pool == nil {
			t.Fatal("expected non-nil cert pool")
		}
	})

	t.Run("invalid-pem", func(t *testing.T) {
		_, err := newX509CertPool([]string{"not a pem certificate"})
		if err == nil {
			t.Fatal("expected error for invalid PEM")
		}
	})

	t.Run("multiple-certs", func(t *testing.T) {
		pool, err := newX509CertPool([]string{utilsTestCertPEM, utilsTestCertPEM})
		if err != nil {
			t.Fatal(err)
		}
		if pool == nil {
			t.Fatal("expected non-nil cert pool")
		}
	})
}

func TestSetLogger(t *testing.T) {
	l := slog.NewLogger()
	tests := []struct {
		name    string
		log     string
		wantErr bool
	}{
		{name: "default-empty", log: ""},
		{name: "stdout", log: "stdout"},
		{name: "stderr", log: "stderr"},
		{name: "discard", log: "discard"},
		{name: "unknown", log: "somewhere", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &File{Log: tt.log}
			err := cfg.SetLogger(l)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("SetLogger() error = %v", err)
			}
		})
	}
}

func TestConnectTimeout(t *testing.T) {
	cfg := &File{}
	cfg.Mux.ConnectTimeout = 12
	if got := cfg.ConnectTimeout(); got != 12*time.Second {
		t.Fatalf("ConnectTimeout() = %v, want %v", got, 12*time.Second)
	}
}

func TestLogWrapperWrite(t *testing.T) {
	t.Run("error-prefix", func(t *testing.T) {
		var buf bytes.Buffer
		l := slog.NewLogger()
		l.SetLevel(slog.LevelError)
		l.SetOutput(slog.OutputWriter, &buf)
		w := &logWrapper{Logger: l}
		n, err := w.Write([]byte("[ERR] boom\n"))
		if err != nil {
			t.Fatal(err)
		}
		if n != len("[ERR] boom\n") {
			t.Fatalf("n = %d, want %d", n, len("[ERR] boom\\n"))
		}
		if !strings.Contains(buf.String(), "boom") {
			t.Fatalf("log output %q does not contain %q", buf.String(), "boom")
		}
	})

	t.Run("warn-prefix", func(t *testing.T) {
		var buf bytes.Buffer
		l := slog.NewLogger()
		l.SetLevel(slog.LevelWarning)
		l.SetOutput(slog.OutputWriter, &buf)
		w := &logWrapper{Logger: l}
		_, err := w.Write([]byte("[WARN] watch\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "watch") {
			t.Fatalf("log output %q does not contain %q", buf.String(), "watch")
		}
	})

	t.Run("no-prefix", func(t *testing.T) {
		var buf bytes.Buffer
		l := slog.NewLogger()
		l.SetLevel(slog.LevelError)
		l.SetOutput(slog.OutputWriter, &buf)
		w := &logWrapper{Logger: l}
		_, err := w.Write([]byte("plain message\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "plain message") {
			t.Fatalf("log output %q does not contain %q", buf.String(), "plain message")
		}
	})
}

func TestNewTLSConfigInvalidAuthCert(t *testing.T) {
	cfg := &File{
		TLS: &TLS{
			Certificate: utilsTestCertPEM,
			PrivateKey:  utilsTestKeyPEM,
			AuthCerts:   []string{"not-a-pem"},
		},
	}
	_, err := cfg.NewTLSConfig("example.com")
	if err == nil {
		t.Fatal("expected error for invalid auth cert")
	}
}
