// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	sd "github.com/hexian000/gosnippets/systemd"
	"github.com/hexian000/tlswrapper/v3/config"
)

var (
	Version  = "dev"
	Homepage = "https://github.com/hexian000/tlswrapper"
)

func init() {
	std := slog.Default()
	if _, file, _, ok := runtime.Caller(0); ok {
		std.SetFilePrefix(filepath.Dir(file) + "/")
	}
	std.SetOutput(slog.OutputWriter, os.Stdout)
	std.SetLevel(slog.LevelVerbose)
}

// AppFlags holds the command-line arguments
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

var appFlags AppFlags

// AppMain is the main entry point of the application
func AppMain(f *AppFlags) int {
	if err := f.Validate(); err != nil {
		slog.Fatalf("arguments: %s", formats.Error(err))
		slog.Infof("try \"%s -h\" for more information", os.Args[0])
		return 1
	}
	if f.Help {
		fmt.Printf("tlswrapper %s\n  %s\n\n", Version, Homepage)
		flag.Usage()
		return 1
	}
	if f.GenCerts != "" {
		return genCerts(f)

	}
	if f.DumpConfig {
		return dumpConfig(f)
	}
	appFlags = *f
	cfg, err := config.LoadFile(f.Config)
	if err != nil {
		slog.Fatal("load config: ", formats.Error(err))
		os.Exit(1)
	}
	slog.Debugf("runtime: %s", runtime.Version())
	server, err := NewServer(cfg)
	if err != nil {
		slog.Fatal("server init: ", formats.Error(err))
		os.Exit(1)
	}
	if err := server.Start(); err != nil {
		slog.Fatal("server start: ", formats.Error(err))
		os.Exit(1)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	signal.Ignore(syscall.SIGPIPE)
	slog.Notice("server start")
	_, _ = sd.Notify(sd.Ready)
	for sig := range ch {
		slog.Debug("got signal: ", sig)
		if sig != syscall.SIGHUP {
			_, _ = sd.Notify(sd.Stopping)
			break
		}
		// reload
		_, _ = sd.Notify(sd.Reloading)
		cfg, err := config.LoadFile(f.Config)
		if err != nil {
			slog.Error("read config: ", formats.Error(err))
			continue
		}
		if err := server.LoadConfig(cfg); err != nil {
			slog.Error("load config: ", formats.Error(err))
			continue
		}
		_, _ = sd.Notify(sd.Ready)
	}

	slog.Notice("server stop")
	if err := server.Shutdown(); err != nil {
		slog.Fatal("server shutdown: ", formats.Error(err))
		return 1
	}
	return 0
}
