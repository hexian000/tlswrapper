package tlswrapper

import (
	"io"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
)

var (
	Version  = "dev"
	Homepage = "https://github.com/hexian000/tlswrapper"
)

func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Warningf("close: %s", formats.Error(err))
	}
}
