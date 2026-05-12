// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hexian000/gosnippets/slog"
)

func captureGrpcLogs(t *testing.T, level slog.Level, fn func(*grpcLogger)) string {
	t.Helper()
	logger := slog.Default()
	prevLevel := logger.Level()
	var buf bytes.Buffer
	logger.SetOutput(slog.OutputWriter, &buf)
	logger.SetLevel(level)
	defer func() {
		logger.SetOutput(slog.OutputDiscard)
		logger.SetLevel(prevLevel)
	}()
	fn(&grpcLogger{})
	return buf.String()
}

func TestGrpcLoggerV(t *testing.T) {
	g := &grpcLogger{}

	logger := slog.Default()
	prevLevel := logger.Level()
	defer logger.SetLevel(prevLevel)

	logger.SetLevel(slog.LevelWarning)
	if !g.V(0) {
		t.Fatal("V(0) should be enabled at warning level")
	}
	if g.V(2) {
		t.Fatal("V(2) should be disabled at warning level")
	}
	if g.V(3) {
		t.Fatal("V(3) should be disabled at warning level")
	}

	logger.SetLevel(slog.LevelInfo)
	if !g.V(2) {
		t.Fatal("V(2) should be enabled at info level")
	}
	if g.V(3) {
		t.Fatal("V(3) should be disabled at info level")
	}

	logger.SetLevel(slog.LevelDebug)
	if !g.V(3) {
		t.Fatal("V(3) should be enabled at debug level")
	}
}

func TestGrpcLoggerLevelGating(t *testing.T) {
	out := captureGrpcLogs(t, slog.LevelError, func(g *grpcLogger) {
		g.Info("info-msg")
		g.Infoln("infoln-msg")
		g.Infof("infof-%d", 1)
		g.Warning("warn-msg")
		g.Warningln("warnln-msg")
		g.Warningf("warnf-%d", 1)
		g.Error("error-msg")
		g.Errorln("errorln-msg")
		g.Errorf("errorf-%d", 1)
		g.ErrorDepth(1, "errordepth-msg")
	})

	for _, want := range []string{"error-msg", "errorln-msg", "errorf-1", "errordepth-msg"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
	for _, notWant := range []string{"info-msg", "infoln-msg", "infof-1", "warn-msg", "warnln-msg", "warnf-1"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("output %q unexpectedly contains %q", out, notWant)
		}
	}
}

func TestGrpcLoggerFatalPaths(t *testing.T) {
	out := captureGrpcLogs(t, slog.LevelFatal, func(g *grpcLogger) {
		g.Fatal("fatal-msg")
		g.Fatalln("fatalln-msg")
		g.Fatalf("fatalf-%d", 1)
		g.FatalDepth(1, "fataldepth-msg")
	})
	for _, want := range []string{"fatal-msg", "fatalln-msg", "fatalf-1", "fataldepth-msg"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
}

func TestGrpcLoggerDepthMethods(t *testing.T) {
	out := captureGrpcLogs(t, slog.LevelInfo, func(g *grpcLogger) {
		g.InfoDepth(1, "infodepth-msg")
		g.WarningDepth(1, "warningdepth-msg")
		g.Warningf("warningf-%d", 2)
		g.Infof("infof-%d", 2)
	})
	for _, want := range []string{"infodepth-msg", "warningdepth-msg", "warningf-2", "infof-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
}

// TestGrpcLoggerPassThrough covers the slog.Println/Printf calls inside Info,
// Infoln, Warning, and Warningln when the level gate is open.
func TestGrpcLoggerPassThrough(t *testing.T) {
	out := captureGrpcLogs(t, slog.LevelInfo, func(g *grpcLogger) {
		g.Info("info-pass")
		g.Infoln("infoln-pass")
		g.Warning("warn-pass")
		g.Warningln("warnln-pass")
	})
	for _, want := range []string{"info-pass", "infoln-pass", "warn-pass", "warnln-pass"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
}
