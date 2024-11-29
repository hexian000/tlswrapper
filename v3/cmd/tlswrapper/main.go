package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3"
)

var flagAlias = map[string]string{
	"help":   "h",
	"config": "c",
}

func parseFlags(f *tlswrapper.AppFlags) {
	flag.BoolVar(&f.Help, "help", false, "show usage and exit")
	flag.StringVar(&f.Config, "config", "", "config file")
	flag.BoolVar(&f.DumpConfig, "dumpconfig", false, "dump config file to stdout")
	flag.StringVar(&f.ServerName, "sni", "example.com", "server name")
	flag.StringVar(&f.GenCerts, "gencerts", "", "comma-separated name list, generate key pairs as <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.Sign, "sign", "", "sign the certificate with <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.KeyType, "keytype", "rsa", "one of rsa, ecdsa, ed25519")
	flag.IntVar(&f.KeySize, "keysize", 0, "specifies the size of the private key, depending on the key type")
	for from, to := range flagAlias {
		flagSet := flag.Lookup(from)
		flag.Var(flagSet.Value, to, fmt.Sprintf("alias to %q", flagSet.Name))
	}
	flag.Parse()
}

func main() {
	f := &tlswrapper.AppFlags{}
	parseFlags(f)
	if code := tlswrapper.AppMain(f); code != 0 {
		os.Exit(code)
	}
	/* tribute to the DOS command "DEBUG" */
	slog.Debug("program terminated normally")
}
