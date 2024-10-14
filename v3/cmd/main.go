package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	sd "github.com/hexian000/gosnippets/systemd"
	"github.com/hexian000/tlswrapper/v3"
	"github.com/hexian000/tlswrapper/v3/config"
)

func init() {
	slog.Default().SetFilePrefix("github.com/hexian000/tlswrapper/v3/")
	slog.Default().SetOutputConfig("stdout", "tlswrapper")
}

func parseFlags(f *tlswrapper.AppFlags) {
	flag.BoolVar(&f.Help, "h", false, "help")
	flag.StringVar(&f.Config, "c", "", "config file")
	flag.BoolVar(&f.DumpConfig, "dumpconfig", false, "dump config file to stdout")
	flag.StringVar(&f.ServerName, "sni", "example.com", "server name")
	flag.StringVar(&f.GenCerts, "gencerts", "", "comma-separated name list, generate key pairs as <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.Sign, "sign", "", "sign the certificate with <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.KeyType, "keytype", "rsa", "one of rsa, ecdsa, ed25519")
	flag.IntVar(&f.KeySize, "keysize", 0, "specify the number of bits for the private key")
	flag.Parse()
}

func main() {
	f := &tlswrapper.Flags
	parseFlags(f)
	if err := f.Validate(); err != nil {
		slog.Fatalf("arguments: %s", formats.Error(err))
		slog.Infof("try \"%s -h\" for more information", os.Args[0])
		os.Exit(1)
	}
	if f.Help {
		fmt.Printf("tlswrapper %s\n  %s\n\n", tlswrapper.Version, tlswrapper.Homepage)
		flag.Usage()
		os.Exit(1)
	}
	if f.GenCerts != "" {
		genCerts(f)
		return
	}
	if f.DumpConfig {
		dumpConfig(f)
		return
	}
	cfg, err := config.LoadFile(f.Config)
	if err != nil {
		slog.Fatal("load config: ", formats.Error(err))
		os.Exit(1)
	}
	slog.Debugf("runtime: %s", runtime.Version())
	server, err := tlswrapper.NewServer(cfg)
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
		slog.Notice("config successfully reloaded")
	}

	slog.Notice("server stop")
	if err := server.Shutdown(); err != nil {
		slog.Fatal("server shutdown: ", formats.Error(err))
		os.Exit(1)
	}

	slog.Info("program terminated normally.")
}
