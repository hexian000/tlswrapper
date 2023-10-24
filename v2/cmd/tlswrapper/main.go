package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/hexian000/tlswrapper/v2"
	"github.com/hexian000/tlswrapper/v2/daemon"
	"github.com/hexian000/tlswrapper/v2/slog"
)

func parseFlags() string {
	var flagHelp bool
	var flagConfig string
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		fmt.Printf("tlswrapper %s\n  %s\n\n", tlswrapper.Version, tlswrapper.Homepage)
		flag.Usage()
		os.Exit(1)
	}
	return flagConfig
}

func readConfig(path string) (*tlswrapper.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := tlswrapper.DefaultConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	slog.Default().SetLevel(cfg.LogLevel)
	if err := slog.Default().SetOutputConfig(cfg.Log, "tlswrapper"); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	path := parseFlags()
	cfg, err := readConfig(path)
	if err != nil {
		slog.Fatal("read config: ", err)
		os.Exit(1)
	}
	slog.Debugf("runtime: %s", runtime.Version())
	server := tlswrapper.NewServer(cfg)
	if err := server.LoadConfig(cfg); err != nil {
		slog.Fatal("load config: ", err)
		os.Exit(1)
	}
	if err := server.Start(); err != nil {
		slog.Fatal("server start: ", err)
		os.Exit(1)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	signal.Ignore(syscall.SIGPIPE)
	slog.Info("server start")
	_, _ = daemon.Notify(daemon.Ready)
	for sig := range ch {
		slog.Verbose("got signal: ", sig)
		if sig != syscall.SIGHUP {
			_, _ = daemon.Notify(daemon.Stopping)
			break
		}
		// reload
		_, _ = daemon.Notify(daemon.Reloading)
		cfg, err := readConfig(path)
		if err != nil {
			slog.Error("read config: ", err)
			continue
		}
		if err := server.LoadConfig(cfg); err != nil {
			slog.Error("load config: ", err)
			continue
		}
		_, _ = daemon.Notify(daemon.Ready)
		slog.Info("config successfully reloaded")
	}

	slog.Info("server stop")
	if err := server.Shutdown(); err != nil {
		slog.Fatal("server shutdown: ", err)
		os.Exit(1)
	}

	slog.Info("program terminated normally.")
}
