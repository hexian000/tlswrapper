package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/hexian000/gosnippets/slog"
)

func Load(b []byte) (*File, error) {
	cfg := Default
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	for name := range cfg.Peers {
		cfg.Peers[name] = TunnelDefault
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
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

func (c *File) Validate() error {
	// TODO
	// if err := rangeCheckInt("keepalive", c.KeepAlive, 0, 86400); err != nil {
	// 	return err
	// }
	// if err := rangeCheckInt("serverkeepalive", c.ServerKeepAlive, 0, 86400); err != nil {
	// 	return err
	// }
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
