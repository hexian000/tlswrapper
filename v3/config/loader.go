// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"os"
	"strings"

	"github.com/hexian000/gosnippets/slog"
)

func loadPEM(s string) (string, error) {
	if strings.HasPrefix(s, "@") {
		certPEM, err := os.ReadFile(strings.TrimPrefix(s, "@"))
		if err != nil {
			return "", err
		}
		return string(certPEM), nil
	}
	return s, nil
}

func (c *KeyPair) Load() error {
	certPEM, err := loadPEM(c.Certificate)
	if err != nil {
		return err
	}
	c.Certificate = certPEM
	keyPEM, err := loadPEM(c.PrivateKey)
	if err != nil {
		return err
	}
	c.PrivateKey = keyPEM
	return nil
}

func (p CertPool) Load() error {
	for i, cert := range p {
		certPEM, err := loadPEM(cert)
		if err != nil {
			return err
		}
		p[i] = certPEM
	}
	return nil
}

func (cfg *File) load() error {
	for i, pair := range cfg.Certificates {
		if err := pair.Load(); err != nil {
			return err
		}
		cfg.Certificates[i] = pair
	}
	if err := cfg.AuthorizedCerts.Load(); err != nil {
		return err
	}
	return nil
}

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
	if slog.CheckLevel(slog.LevelVeryVerbose) {
		if b, err := cfg.Dump(); err == nil {
			slog.Text(slog.LevelVeryVerbose, string(b), "load config")
		}
	}
	return &cfg, nil
}

func LoadFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(b)
}

func rangeCheckInt(key string, value int, min int, max int) error {
	if !(min <= value && value <= max) {
		return fmt.Errorf("%s is out of range (%d - %d)", key, min, max)
	}
	return nil
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

func (c *File) Validate() error {
	if err := checkType(c.Type); err != nil {
		return err
	}
	if err := rangeCheckInt("keepalive", c.KeepAlive, 0, 86400); err != nil {
		return err
	}
	if err := rangeCheckInt("serverkeepalive", c.ServerKeepAlive, 0, 86400); err != nil {
		return err
	}
	if err := rangeCheckInt("startuplimitstart", c.StartupLimitStart, 1, math.MaxInt); err != nil {
		return err
	}
	if err := rangeCheckInt("startuplimitrate", c.StartupLimitRate, 0, 100); err != nil {
		return err
	}
	if err := rangeCheckInt("startuplimitfull", c.StartupLimitFull, 1, math.MaxInt); err != nil {
		return err
	}
	if err := rangeCheckInt("maxconn", c.MaxConn, 1, math.MaxInt); err != nil {
		return err
	}
	if err := rangeCheckInt("maxsessions", c.MaxSessions, 1, math.MaxInt); err != nil {
		return err
	}
	return nil
}

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

func (cfg *File) Dump() ([]byte, error) {
	c, err := cfg.Clone()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(c, "", "  ")
}
