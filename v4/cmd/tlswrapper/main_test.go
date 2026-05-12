// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package main

import (
	"flag"
	"io"
	"os"
	"testing"

	"github.com/hexian000/tlswrapper/v4"
)

func runParseFlagsForTest(t *testing.T, args []string) tlswrapper.AppFlags {
	t.Helper()
	origArgs := os.Args
	origFlagSet := flag.CommandLine
	defer func() {
		os.Args = origArgs
		flag.CommandLine = origFlagSet
	}()

	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flag.CommandLine = fs
	os.Args = args

	var got tlswrapper.AppFlags
	parseFlags(&got)
	return got
}

func TestParseFlagsDefaults(t *testing.T) {
	got := runParseFlagsForTest(t, []string{"tlswrapper"})
	if got.Help {
		t.Fatal("Help = true, want false")
	}
	if got.Color {
		t.Fatal("Color = true, want false")
	}
	if got.Config != "" {
		t.Fatalf("Config = %q, want empty", got.Config)
	}
	if got.ServerName != "example.com" {
		t.Fatalf("ServerName = %q, want %q", got.ServerName, "example.com")
	}
	if got.KeyType != "rsa" {
		t.Fatalf("KeyType = %q, want %q", got.KeyType, "rsa")
	}
	if got.LogLevel != -1 {
		t.Fatalf("LogLevel = %d, want -1", got.LogLevel)
	}
}

func TestParseFlagsLongOptions(t *testing.T) {
	got := runParseFlagsForTest(t, []string{
		"tlswrapper",
		"-help",
		"-color",
		"-config", "cfg.json",
		"-dumpconfig",
		"-sni", "server.example",
		"-gencerts", "a,b",
		"-sign", "ca",
		"-keytype", "ed25519",
		"-keysize", "4096",
		"-loglevel", "6",
	})
	if !got.Help || !got.Color || !got.DumpConfig {
		t.Fatalf("expected Help/Color/DumpConfig all true, got %+v", got)
	}
	if got.Config != "cfg.json" {
		t.Fatalf("Config = %q, want %q", got.Config, "cfg.json")
	}
	if got.ServerName != "server.example" {
		t.Fatalf("ServerName = %q, want %q", got.ServerName, "server.example")
	}
	if got.GenCerts != "a,b" {
		t.Fatalf("GenCerts = %q, want %q", got.GenCerts, "a,b")
	}
	if got.Sign != "ca" {
		t.Fatalf("Sign = %q, want %q", got.Sign, "ca")
	}
	if got.KeyType != "ed25519" {
		t.Fatalf("KeyType = %q, want %q", got.KeyType, "ed25519")
	}
	if got.KeySize != 4096 {
		t.Fatalf("KeySize = %d, want 4096", got.KeySize)
	}
	if got.LogLevel != 6 {
		t.Fatalf("LogLevel = %d, want 6", got.LogLevel)
	}
}

func TestParseFlagsAliases(t *testing.T) {
	got := runParseFlagsForTest(t, []string{
		"tlswrapper",
		"-h",
		"-C",
		"-c", "alias.json",
	})
	if !got.Help {
		t.Fatal("Help = false, want true")
	}
	if !got.Color {
		t.Fatal("Color = false, want true")
	}
	if got.Config != "alias.json" {
		t.Fatalf("Config = %q, want %q", got.Config, "alias.json")
	}
}
