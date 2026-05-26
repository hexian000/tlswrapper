package tlswrapper

import (
	"bytes"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hexian000/gosnippets/slog"
)

func TestAppFlagsValidate(t *testing.T) {
	tests := []struct {
		name    string
		flags   AppFlags
		wantErr bool
	}{
		{name: "help", flags: AppFlags{Help: true}},
		{name: "gencerts", flags: AppFlags{GenCerts: "peer"}},
		{name: "dumpconfig", flags: AppFlags{DumpConfig: true}},
		{name: "missing-config", flags: AppFlags{}, wantErr: true},
		{name: "config-present", flags: AppFlags{Config: "server.json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.flags.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestAppMainHelp(t *testing.T) {
	oldUsage := flag.Usage
	flag.Usage = func() {}
	defer func() { flag.Usage = oldUsage }()

	var code int
	out := captureStdout(t, func() {
		code = AppMain(&AppFlags{Help: true})
	})
	if code != 1 {
		t.Fatalf("AppMain(help) = %d, want 1", code)
	}
	if !strings.Contains(out, Homepage) {
		t.Fatalf("output %q does not contain %q", out, Homepage)
	}
}

func TestAppMainDumpConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := newTestConfig(t, map[string]any{"api_listen": "127.0.0.1:9000"})
	b, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}

	var code int
	captureStdout(t, func() {
		code = AppMain(&AppFlags{DumpConfig: true, Config: path})
	})
	if code != 0 {
		t.Fatalf("AppMain(dumpconfig) = %d, want 0", code)
	}
}

func TestAppMainGenCerts(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	code := AppMain(&AppFlags{GenCerts: "peer", ServerName: "example.com", KeyType: "ed25519"})
	if code != 0 {
		t.Fatalf("AppMain(gencerts) = %d, want 0", code)
	}
	for _, name := range []string{"peer-cert.pem", "peer-key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("Stat(%q): %v", name, err)
		}
	}
}

func TestAppMainProcessHelper(t *testing.T) {
	if os.Getenv("TLSWRAPPER_APPMAIN_HELPER") != "1" {
		return
	}
	code := AppMain(&AppFlags{
		Config:   os.Getenv("TLSWRAPPER_APPMAIN_CONFIG"),
		LogLevel: int(slog.LevelError),
	})
	os.Exit(code)
}

func startAppMainProcess(t *testing.T) (*exec.Cmd, string, *bytes.Buffer) {
	t.Helper()
	apiAddr := freePort(t)
	path := filepath.Join(t.TempDir(), "config.json")
	b, err := newTestConfig(t, map[string]any{"api_listen": apiAddr}).Dump()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppMainProcessHelper$")
	cmd.Env = append(os.Environ(),
		"TLSWRAPPER_APPMAIN_HELPER=1",
		"TLSWRAPPER_APPMAIN_CONFIG="+path,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	client := &http.Client{Timeout: 500 * time.Millisecond}
	waitFor(t, 5*time.Second, func() bool {
		resp, err := client.Get("http://" + apiAddr + "/healthy")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return cmd, apiAddr, &output
}

func TestAppMainLifecycle(t *testing.T) {
	cmd, _, output := startAppMainProcess(t)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() = %v, output: %s", err, output.String())
	}
	cmd.Process = nil
}

func TestAppMainReloadOnSIGHUP(t *testing.T) {
	cmd, apiAddr, output := startAppMainProcess(t)
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	waitFor(t, 5*time.Second, func() bool {
		resp, err := client.Get("http://" + apiAddr + "/healthy")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() = %v, output: %s", err, output.String())
	}
	cmd.Process = nil
}

// TestAppMainValidationFailure verifies that AppMain returns 1 when no config
// file is specified and validation fails.
func TestAppMainValidationFailure(t *testing.T) {
	var code int
	captureStdout(t, func() {
		code = AppMain(&AppFlags{})
	})
	if code != 1 {
		t.Fatalf("AppMain(empty flags) = %d, want 1", code)
	}
}

// TestAppMainColor verifies that AppMain processes the Color flag before
// validation, and still returns 1 when no config is set.
func TestAppMainColor(t *testing.T) {
	var code int
	captureStdout(t, func() {
		code = AppMain(&AppFlags{Color: true})
	})
	// Restore slog to a safe writer: AppMain(Color=true) set it to OutputTerminal
	// pointing at the now-closed captured pipe.
	slog.Default().SetOutput(slog.OutputWriter, os.Stdout)
	if code != 1 {
		t.Fatalf("AppMain(Color=true, no config) = %d, want 1", code)
	}
}

// TestAppMainDumpConfigDefault verifies that AppMain outputs the default config
// and returns 0 when DumpConfig is true and no Config path is supplied.
func TestAppMainDumpConfigDefault(t *testing.T) {
	var code int
	captureStdout(t, func() {
		code = AppMain(&AppFlags{DumpConfig: true})
	})
	if code != 0 {
		t.Fatalf("AppMain(DumpConfig=true, no config path) = %d, want 0", code)
	}
}
