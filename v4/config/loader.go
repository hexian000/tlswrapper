// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"os"
	"strconv"
	"strings"

	"github.com/hexian000/gosnippets/slog"
)

// loadPEM resolves "@path" PEM references used by TLS fields.
func loadPEM(s string) (string, error) {
	if fileName, ok := strings.CutPrefix(s, "@"); ok {
		certPEM, err := os.ReadFile(fileName)
		if err != nil {
			return "", err
		}
		return string(certPEM), nil
	}
	return s, nil
}

// load resolves any "@path" references in the TLS section.
func (t *TLS) load() error {
	certPEM, err := loadPEM(t.Certificate)
	if err != nil {
		return err
	}
	t.Certificate = certPEM
	keyPEM, err := loadPEM(t.PrivateKey)
	if err != nil {
		return err
	}
	t.PrivateKey = keyPEM
	for i, cert := range t.AuthCerts {
		certPEM, err := loadPEM(cert)
		if err != nil {
			return err
		}
		t.AuthCerts[i] = certPEM
	}
	return nil
}

func clampInt(v *int, min, max int) {
	if *v < min {
		*v = min
	} else if *v > max {
		*v = max
	}
}

func (cfg *File) load() error {
	if cfg.TLS != nil {
		if err := cfg.TLS.load(); err != nil {
			return err
		}
	}
	return nil
}

// Load decodes, validates, and normalizes one config snapshot.
func Load(b []byte) (*File, error) {
	cfg := Default
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.load(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.SetLogger(slog.Default()); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadFile reads path and delegates to Load.
func LoadFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(b)
}

func checkType(s string) error {
	mediatype, params, err := mime.ParseMediaType(s)
	if err != nil {
		return fmt.Errorf("invalid config type: %q", s)
	}
	if mediatype != mimeType {
		return fmt.Errorf("invalid config type: %q", s)
	}
	version, ok := params["version"]
	if !ok {
		return fmt.Errorf("invalid config type: %q", s)
	}
	if version != mimeVersion {
		return fmt.Errorf("incompatible config version: %q", version)
	}
	return nil
}

// parseMaxStartups parses the "start:rate:full" throttle string
func parseMaxStartups(s string) (start, rate, full int, err error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		err = fmt.Errorf("invalid format %q, expected start:rate:full", s)
		return
	}
	start, err = strconv.Atoi(parts[0])
	if err != nil || start < 1 {
		err = fmt.Errorf("start must be a positive integer, got %q", parts[0])
		return
	}
	rate, err = strconv.Atoi(parts[1])
	if err != nil || rate < 0 || rate > 100 {
		err = fmt.Errorf("rate must be 0-100, got %q", parts[1])
		return
	}
	full, err = strconv.Atoi(parts[2])
	if err != nil || full < start {
		err = fmt.Errorf("full must be >= start, got %q", parts[2])
		return
	}
	return
}

// ParsedMaxStartups parses MaxStartups, or returns zeros when it is empty.
func (c *File) ParsedMaxStartups() (start, rate, full int) {
	if c.MaxStartups == "" {
		return
	}
	start, rate, full, _ = parseMaxStartups(c.MaxStartups)
	return
}

// Validate checks declared values and clamps tunables into supported ranges.
func (c *File) Validate() error {
	if err := checkType(c.Type); err != nil {
		return err
	}
	if c.MaxStartups != "" {
		if _, _, _, err := parseMaxStartups(c.MaxStartups); err != nil {
			return fmt.Errorf("max_startups: %w", err)
		}
	}
	// clamp timing fields
	clampInt(&c.Mux.PingTimeout, 10, 86400)
	clampInt(&c.Mux.KeepAlive, 10, 86400)
	clampInt(&c.Mux.SendTimeout, 10, 86400)
	clampInt(&c.Mux.ConnectTimeout, 10, 86400)
	if c.Mux.IdleTimeout != 0 {
		clampInt(&c.Mux.IdleTimeout, 10, 86400)
	}
	if c.Mux.SessionWindow != 0 {
		clampInt(&c.Mux.SessionWindow, 65535, math.MaxInt32)
	}
	if c.Mux.StreamWindow != 0 {
		clampInt(&c.Mux.StreamWindow, 65535, math.MaxInt32)
	}
	if c.Mux.TCP.ReadBuffer != 0 {
		clampInt(&c.Mux.TCP.ReadBuffer, 1, math.MaxInt32)
	}
	if c.Mux.TCP.WriteBuffer != 0 {
		clampInt(&c.Mux.TCP.WriteBuffer, 1, math.MaxInt32)
	}
	clampInt(&c.Mux.TCP.Backlog, 1, 4096)
	if c.TCP.ReadBuffer != 0 {
		clampInt(&c.TCP.ReadBuffer, 1, math.MaxInt32)
	}
	if c.TCP.WriteBuffer != 0 {
		clampInt(&c.TCP.WriteBuffer, 1, math.MaxInt32)
	}
	clampInt(&c.TCP.Backlog, 1, 4096)
	return nil
}

// Clone deep-copies the configuration.
func (cfg *File) Clone() (*File, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	c := &File{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Dump returns the fully loaded config as indented JSON.
func (cfg *File) Dump() ([]byte, error) {
	c, err := cfg.Clone()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(c, "", "  ")
}
