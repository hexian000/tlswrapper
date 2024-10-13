package config

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
)

func ParsePEM(data []byte, blockType string) []byte {
	var p *pem.Block
	b := data
	for {
		p, b = pem.Decode(b)
		if p == nil || p.Type == blockType {
			return p.Bytes
		}
	}
}

func readX509File(path string, blockType string) ([]byte, error) {
	switch filepath.Ext(path) {
	case ".pem":
		certPEM, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		certDER := ParsePEM(certPEM, blockType)
		if certDER == nil {
			return nil, fmt.Errorf("%s: %q not found", blockType, path)
		}
		return certDER, nil
	case ".der":
		certDER, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return certDER, nil
	}
	return nil, fmt.Errorf("%s: supported formats are .pem, .der", path)

}

func loadX509(s string, blockType string) ([]byte, error) {
	if strings.HasPrefix(s, "=") {
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "="))
	}
	if strings.HasPrefix(s, "@") {
		certDER, err := readX509File(strings.TrimPrefix(s, "@"), blockType)
		if err != nil {
			return nil, err
		}
		return certDER, nil
	}
	certDER := ParsePEM([]byte(s), blockType)
	if certDER == nil {
		return nil, errors.New("unable to parse PEM")
	}
	return certDER, nil
}

func (c *KeyPair) Load() error {
	certDER, err := loadX509(c.Certificate, "CERTIFICATE")
	if err != nil {
		return err
	}
	keyDER, err := loadX509(c.PrivateKey, "PRIVATE KEY")
	if err != nil {
		return err
	}
	c.Certificate = "=" + base64.StdEncoding.EncodeToString(certDER)
	c.PrivateKey = "=" + base64.StdEncoding.EncodeToString(keyDER)
	return nil
}

func (p CertPool) Load() error {
	for i, cert := range p {
		certDER, err := loadX509(cert, "CERTIFICATE")
		if err != nil {
			return err
		}
		p[i] = "=" + base64.StdEncoding.EncodeToString(certDER)
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
	slog.Default().SetLevel(cfg.LogLevel)
	if err := slog.Default().SetOutputConfig(cfg.Log, "tlswrapper"); err != nil {
		return nil, err
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

func (c *Tunnel) Validate() error {
	if c.Disabled {
		return nil
	}
	if c.MuxDial == "" && c.Listen == "" && c.Service == "" {
		return errors.New("empty tunnel config")
	}
	return nil
}

func (c *File) Validate() error {
	for name, tuncfg := range c.Peers {
		if err := tuncfg.Validate(); err != nil {
			return fmt.Errorf("tunnel %q: %s", name, formats.Error(err))
		}
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
