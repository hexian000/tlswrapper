package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hexian000/tlswrapper/daemon"
	"github.com/hexian000/tlswrapper/slog"
)

var (
	version  = "dev-build"
	homepage = "https://github.com/hexian000/tlswrapper"
)

func init() {
	fmt.Printf("tlswrapper %s\n  %s\n", version, homepage)
}

func parseFlags() string {
	var flagHelp bool
	var flagConfig string
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		flag.Usage()
		os.Exit(1)
	}
	return flagConfig
}

func readConfig(path string) (*Config, error) {
	slog.Verbose("read config:", path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	path := parseFlags()
	cfg, err := readConfig(path)
	if err != nil {
		slog.Fatal("read config:", err)
		os.Exit(1)
	}
	slog.Default().SetLevel(cfg.LogLevel)
	if err := slog.Default().SetOutputConfig(cfg.Log, "tlswrapper"); err != nil {
		slog.Fatal("logging:", err)
		os.Exit(1)
	}
	server := NewServer()
	if err := server.LoadConfig(cfg); err != nil {
		slog.Fatal("load config:", err)
		os.Exit(1)
	}
	slog.Info("server starting")
	if err := server.Start(); err != nil {
		slog.Fatal("server start:", err)
		os.Exit(1)
	}
	_, _ = daemon.Notify(daemon.Ready)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for {
		sig := <-ch
		slog.Verbose("got signal:", sig)
		if sig != syscall.SIGHUP {
			_, _ = daemon.Notify(daemon.Stopping)
			break
		}
		// reload
		_, _ = daemon.Notify(daemon.Reloading)
		cfg, err := readConfig(path)
		if err != nil {
			slog.Error("read config:", err)
			continue
		}
		slog.Default().SetLevel(cfg.LogLevel)
		if err := slog.Default().SetOutputConfig(cfg.Log, "tlswrapper"); err != nil {
			slog.Error("logging:", err)
			continue
		}
		if err := server.LoadConfig(cfg); err != nil {
			slog.Error("load config:", err)
			continue
		}
		_, _ = daemon.Notify(daemon.Ready)
		slog.Info("config successfully reloaded")
	}

	if err := server.Shutdown(); err != nil {
		slog.Fatal("server shutdown:", err)
		os.Exit(1)
	}
	slog.Info("server stopped gracefully")
}
