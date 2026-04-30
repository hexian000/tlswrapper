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

// loadPEM loads a PEM string or reads it from a file if it starts with '@'
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

// load resolves any "@path" references in the TLS config
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

// Load loads configuration from a byte slice
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

// LoadFile loads configuration from a file
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

// ParsedMaxStartups returns the parsed components of the MaxStartups throttle string.
// Returns (0,0,0) if MaxStartups is empty.
func (c *File) ParsedMaxStartups() (start, rate, full int) {
	if c.MaxStartups == "" {
		return
	}
	start, rate, full, _ = parseMaxStartups(c.MaxStartups)
	return
}

// Validate validates and clamps the configuration
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
	clampInt(&c.PingTimeout, 5, 86400)
	clampInt(&c.KeepAlive, 1, c.PingTimeout)
	clampInt(&c.SendTimeout, 5, c.PingTimeout)
	if c.IdleTimeout != 0 {
		clampInt(&c.IdleTimeout, 5, 31557600)
	}
	if c.Mux.SessionWindow != 0 {
		clampInt(&c.Mux.SessionWindow, 65535, math.MaxInt32)
	}
	if c.Mux.StreamWindow != 0 {
		clampInt(&c.Mux.StreamWindow, 65535, math.MaxInt32)
	}
	clampInt(&c.TCP.Backlog, 1, 4096)
	return nil
}

// Clone creates a deep copy of the configuration file
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

// Dump dumps the configuration file to JSON format
func (cfg *File) Dump() ([]byte, error) {
	c, err := cfg.Clone()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(c, "", "  ")
}
