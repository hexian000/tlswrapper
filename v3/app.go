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
	ServerName string
	GenCerts   string
	ImportCert string
	KeySize    int
}

func (f *AppFlags) Validate() error {
	if f.Help {
		return nil
	}
	if f.ImportCert != "" {
		if f.Config == "" {
			return errors.New("`-importcert' requires `-c'")
		}
		return nil
	}
	if f.GenCerts != "" {
		return nil
	}
	// server mode
	if f.Config == "" {
		return errors.New("config file is not specified")
	}
	return nil
}

var Flags AppFlags

func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Output(2, slog.LevelWarning, []byte(msg))
	}
}