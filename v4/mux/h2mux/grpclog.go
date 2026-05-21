// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"github.com/hexian000/gosnippets/slog"
	"google.golang.org/grpc/grpclog"
)

func init() {
	grpclog.SetLoggerV2(&grpcLogger{})
}

// grpcLogger bridges grpclog.LoggerV2 (and DepthLoggerV2) to slog.
// It also satisfies grpclog/internal.DepthLoggerV2 via structural typing,
// so SetLoggerV2 will enable depth-aware caller reporting.
type grpcLogger struct{}

// calldepth used by non-depth methods:
// slog.Printf/Println adds +1, logger.output adds +1, runtime.Caller adds +1,
// so 2 here resolves to the grpc call site one frame above our adapter method.
const grpcLogDepth = 2

func (g *grpcLogger) Info(args ...any) {
	if !slog.CheckLevel(slog.LevelInfo) {
		return
	}
	slog.Println(grpcLogDepth, slog.LevelInfo, nil, args...)
}

func (g *grpcLogger) Infoln(args ...any) {
	if !slog.CheckLevel(slog.LevelInfo) {
		return
	}
	slog.Println(grpcLogDepth, slog.LevelInfo, nil, args...)
}

func (g *grpcLogger) Infof(format string, args ...any) {
	if !slog.CheckLevel(slog.LevelInfo) {
		return
	}
	slog.Printf(grpcLogDepth, slog.LevelInfo, nil, format, args...)
}

func (g *grpcLogger) Warning(args ...any) {
	if !slog.CheckLevel(slog.LevelWarning) {
		return
	}
	slog.Println(grpcLogDepth, slog.LevelWarning, nil, args...)
}

func (g *grpcLogger) Warningln(args ...any) {
	if !slog.CheckLevel(slog.LevelWarning) {
		return
	}
	slog.Println(grpcLogDepth, slog.LevelWarning, nil, args...)
}

func (g *grpcLogger) Warningf(format string, args ...any) {
	if !slog.CheckLevel(slog.LevelWarning) {
		return
	}
	slog.Printf(grpcLogDepth, slog.LevelWarning, nil, format, args...)
}

func (g *grpcLogger) Error(args ...any) {
	if !slog.CheckLevel(slog.LevelError) {
		return
	}
	slog.Println(grpcLogDepth, slog.LevelError, nil, args...)
}

func (g *grpcLogger) Errorln(args ...any) {
	if !slog.CheckLevel(slog.LevelError) {
		return
	}
	slog.Println(grpcLogDepth, slog.LevelError, nil, args...)
}

func (g *grpcLogger) Errorf(format string, args ...any) {
	if !slog.CheckLevel(slog.LevelError) {
		return
	}
	slog.Printf(grpcLogDepth, slog.LevelError, nil, format, args...)
}

func (g *grpcLogger) Fatal(args ...any) {
	slog.Println(grpcLogDepth, slog.LevelFatal, nil, args...)
}

func (g *grpcLogger) Fatalln(args ...any) {
	slog.Println(grpcLogDepth, slog.LevelFatal, nil, args...)
}

func (g *grpcLogger) Fatalf(format string, args ...any) {
	slog.Printf(grpcLogDepth, slog.LevelFatal, nil, format, args...)
}

// V reports whether grpc verbosity level l is enabled.
// grpc uses: 0=errors, 2=info, 4=verbose.
func (g *grpcLogger) V(l int) bool {
	switch {
	case l <= 0:
		return slog.CheckLevel(slog.LevelWarning)
	case l <= 2:
		return slog.CheckLevel(slog.LevelInfo)
	default:
		return slog.CheckLevel(slog.LevelDebug)
	}
}

// InfoDepth, WarningDepth, ErrorDepth, FatalDepth satisfy
// grpclog/internal.DepthLoggerV2 through structural typing.

func (g *grpcLogger) InfoDepth(depth int, args ...any) {
	if !slog.CheckLevel(slog.LevelInfo) {
		return
	}
	slog.Println(depth+grpcLogDepth, slog.LevelInfo, nil, args...)
}

func (g *grpcLogger) WarningDepth(depth int, args ...any) {
	if !slog.CheckLevel(slog.LevelWarning) {
		return
	}
	slog.Println(depth+grpcLogDepth, slog.LevelWarning, nil, args...)
}

func (g *grpcLogger) ErrorDepth(depth int, args ...any) {
	if !slog.CheckLevel(slog.LevelError) {
		return
	}
	slog.Println(depth+grpcLogDepth, slog.LevelError, nil, args...)
}

func (g *grpcLogger) FatalDepth(depth int, args ...any) {
	slog.Println(depth+grpcLogDepth, slog.LevelFatal, nil, args...)
}

// Ensure grpcLogger satisfies grpclog.LoggerV2 at compile time.
var _ grpclog.LoggerV2 = (*grpcLogger)(nil)
