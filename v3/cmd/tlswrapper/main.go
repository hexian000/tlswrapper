package main

import (
	"flag"
	"os"

	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3"
)

func parseFlags(f *tlswrapper.AppFlags) {
	flag.BoolVar(&f.Help, "h", false, "help")
	flag.StringVar(&f.Config, "c", "", "config file")
	flag.BoolVar(&f.DumpConfig, "dumpconfig", false, "dump config file to stdout")
	flag.StringVar(&f.ServerName, "sni", "example.com", "server name")
	flag.StringVar(&f.GenCerts, "gencerts", "", "comma-separated name list, generate key pairs as <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.Sign, "sign", "", "sign the certificate with <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.KeyType, "keytype", "rsa", "one of rsa, ecdsa, ed25519")
	flag.IntVar(&f.KeySize, "keysize", 0, "specifies the size of the private key, depending on the key type")
	flag.Parse()
}

func main() {
	f := &tlswrapper.AppFlags{}
	parseFlags(f)
	if code := tlswrapper.AppMain(f); code != 0 {
		os.Exit(code)
	}
	slog.Debug("program terminated normally.")
}
