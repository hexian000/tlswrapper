package tlswrapper

import (
	"fmt"
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
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Output(2, slog.LevelWarning, []byte(msg))
	}
}
