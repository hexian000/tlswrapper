// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

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
	flag.StringVar(&f.ServerName, "sni", "example.com", "gencerts: server name")
	flag.StringVar(&f.GenCerts, "gencerts", "", "comma-separated name list, generate key pairs as <name>-cert.pem, <name>-key.pem")
	flag.StringVar(&f.Sign, "sign", "", "gencerts: sign the certificate with <signer>-cert.pem, <signer>-key.pem")
	flag.StringVar(&f.KeyType, "keytype", "rsa", "gencerts: one of rsa, ecdsa, ed25519")
	flag.IntVar(&f.KeySize, "keysize", 0, "gencerts: specifies the size of the private key, depending on the key type")
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
