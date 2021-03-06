package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
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
	var flagVerbose bool
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.BoolVar(&flagVerbose, "v", false, "verbose mode")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		flag.Usage()
		os.Exit(1)
	}
	if flagVerbose {
		slog.Default().SetLevel(slog.LevelVerbose)
	} else {
		slog.Default().SetLevel(slog.LevelInfo)
	}
	return flagConfig
}

func readConfig(path string) (*Config, error) {
	slog.Verbose("read config:", path)
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func setUDPLog(addr string) error {
	if addr == "" {
		slog.Default().SetOutput(os.Stderr)
		return nil
	}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return err
	}
	slog.Verbose("logging to", addr)
	slog.Default().SetOutput(conn)
	return nil
}

func main() {
	path := parseFlags()
	cfg, err := readConfig(path)
	if err != nil {
		slog.Fatal("read config:", err)
		os.Exit(1)
	}
	if err := setUDPLog(cfg.UDPLog); err != nil {
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
		newCfg, err := readConfig(path)
		if err != nil {
			slog.Error("read config:", err)
			continue
		}
		if err := setUDPLog(newCfg.UDPLog); err != nil {
			slog.Error("logging:", err)
			continue
		}
		if err := server.LoadConfig(newCfg); err != nil {
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
