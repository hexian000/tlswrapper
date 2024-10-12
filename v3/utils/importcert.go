package utils

import (
	"fmt"
	"os"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/tlswrapper/v3/config"
)

func ImportCert(inCfg string, outCfg string) error {
	cfg, err := config.LoadFile(inCfg)
	if err != nil {
		return fmt.Errorf("load config: %s", formats.Error(err))
	}
	b, err := cfg.Dump()
	if err != nil {
		return fmt.Errorf("dump config: %s", formats.Error(err))
	}
	err = os.WriteFile(outCfg, b, 0600)
	if err != nil {
		return fmt.Errorf("write config: %s", formats.Error(err))
	}
	return nil
}
