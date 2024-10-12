package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/hexian000/gosnippets/slog"
	sd "github.com/hexian000/gosnippets/systemd"
	"github.com/hexian000/tlswrapper/v3"
	"github.com/hexian000/tlswrapper/v3/config"
	"github.com/hexian000/tlswrapper/v3/utils"
)

func init() {
	slog.Default().SetFilePrefix("github.com/hexian000/tlswrapper/v3/")
	slog.Default().SetOutputConfig("stdout", "tlswrapper")
}

func parseFlags() string {
	var flagHelp bool
	var flagConfig string
	var flagConfigOut string
	var flagCertName string
	var flagKeySize int
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.StringVar(&flagConfigOut, "importcert", "", "import PEM files and generate a new config file")
	flag.StringVar(&flagCertName, "genkey", "", "generate key pair as <name>-cert.pem, <name>-key.pem")
	flag.IntVar(&flagKeySize, "keysize", 4096, "bits in RSA private key, default to 4096")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		fmt.Printf("tlswrapper %s\n  %s\n\n", tlswrapper.Version, tlswrapper.Homepage)
		flag.Usage()
		os.Exit(1)
	}
	if flagConfigOut != "" {
		err := utils.ImportCert(flagConfig, flagConfigOut)
		if err != nil {
			slog.Fatal(err.Error())
			os.Exit(1)
		}
		slog.Info("importcert: ok")
		os.Exit(0)
	}
	if flagCertName != "" {
		bits := flagKeySize
		certFile, keyFile := flagCertName+"-cert.pem", flagCertName+"-key.pem"
		slog.Infof("genkey: RSA %d bits...", bits)
		err := utils.GenerateX509KeyPair(bits, certFile, keyFile)
		if err != nil {
			slog.Fatal(err.Error())
			os.Exit(1)
		}
		slog.Infof("genkey: X.509 Certificate=%q, Private Key=%q", certFile, keyFile)
		os.Exit(0)
	}
	return flagConfig
}

func main() {
	path := parseFlags()
	cfg, err := config.LoadFile(path)
	if err != nil {
		slog.Fatal("load config: ", err)
		os.Exit(1)
	}
	slog.Debugf("runtime: %s", runtime.Version())
	server, err := tlswrapper.NewServer(cfg)
	if err != nil {
		slog.Fatal("server init: ", err)
		os.Exit(1)
	}
	if err := server.Start(); err != nil {
		slog.Fatal("server start: ", err)
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
		cfg, err := config.LoadFile(path)
		if err != nil {
			slog.Error("read config: ", err)
			continue
		}
		if err := server.LoadConfig(cfg); err != nil {
			slog.Error("load config: ", err)
			continue
		}
		_, _ = sd.Notify(sd.Ready)
		slog.Notice("config successfully reloaded")
	}

	slog.Notice("server stop")
	if err := server.Shutdown(); err != nil {
		slog.Fatal("server shutdown: ", err)
		os.Exit(1)
	}

	slog.Info("program terminated normally.")
}
