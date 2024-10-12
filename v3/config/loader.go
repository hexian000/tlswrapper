package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/hexian000/gosnippets/slog"
)

func load(cfg *File) error {
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

func infer(cfg *File) {
	for name, tuncfg := range cfg.Peers {
		if !tuncfg.NoRedial {
			tuncfg.NoRedial = cfg.NoRedial
		}
		if tuncfg.KeepAlive == 0 {
			tuncfg.KeepAlive = cfg.KeepAlive
		}
		if tuncfg.AcceptBacklog == 0 {
			tuncfg.AcceptBacklog = cfg.AcceptBacklog
		}
		if tuncfg.StreamWindow == 0 {
			tuncfg.StreamWindow = cfg.StreamWindow
		}
		cfg.Peers[name] = tuncfg
	}
}

func Load(b []byte) (*File, error) {
	cfg := Default
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if err := load(&cfg); err != nil {
		return nil, err
	}
	infer(&cfg)
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
	if err := rangeCheckInt("keepalive", c.KeepAlive, 0, 86400); err != nil {
		return err
	}
	return nil
}

func (c *File) Validate() error {
	for name, tuncfg := range c.Peers {
		if err := tuncfg.Validate(); err != nil {
			return fmt.Errorf("tunnel %q: %s", name, err.Error())
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

func (cfg *File) Dump() ([]byte, error) {
	for i, pair := range cfg.Certificates {
		pair.Certificate, pair.PrivateKey = "", ""
		cfg.Certificates[i] = pair
	}
	for i, cert := range cfg.AuthorizedCerts {
		cert.Certificate = ""
		cfg.AuthorizedCerts[i] = cert
	}
	return json.MarshalIndent(cfg, "", "  ")
}
