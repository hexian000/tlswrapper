package tlswrapper

import (
	"errors"
	"fmt"
	"io"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
)

var (
	Version  = "dev"
	Homepage = "https://github.com/hexian000/tlswrapper"
)

type AppFlags struct {
	Help       bool
	Config     string
	DumpConfig bool
	ServerName string
	GenCerts   string
	Sign       string
	KeyType    string
	KeySize    int
}

func (f *AppFlags) Validate() error {
	if f.Help {
		return nil
	}
	if f.GenCerts != "" {
		return nil
	}
	if f.Config == "" {
		return errors.New("config file is not specified")
	}
	if f.DumpConfig {
		return nil
	}
	return nil
}

var Flags AppFlags

func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Output(2, slog.LevelWarning, msg)
	}
}
