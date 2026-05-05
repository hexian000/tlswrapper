#!/usr/bin/env python3

"""Run Go-native profiling for a tlswrapper module and write a Markdown report."""

from __future__ import annotations

import argparse
import os
import socket
import shlex
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, List, Optional, Sequence


SCRIPT_DIR = Path(__file__).resolve().parent
ROOT = Path.cwd().resolve()
DEFAULT_BUILD_DIR = SCRIPT_DIR.parent / "build"
DEFAULT_PROFILE_DIR = DEFAULT_BUILD_DIR / "goprof"
DEFAULT_OUTPUT = DEFAULT_BUILD_DIR / "goprof.md"
PROFILE_TEST_NAME = "TestProfileIperf3Workload"
PROFILE_TEST_FILE = "zz_goprof_workload_test.go"
COMMAND_TIMEOUT_GRACE_SECONDS = 30.0
BUILD_TIMEOUT_GRACE_SECONDS = 300.0
PROFILE_TIMEOUT_GRACE_SECONDS = 120.0
PPROF_TIMEOUT_GRACE_SECONDS = 120.0

PROFILE_TEST_SOURCE = r'''package tlswrapper

import (
    "context"
    "crypto/ed25519"
    "crypto/rand"
    "io"
    "net"
    "os"
    "os/exec"
    "strconv"
    "testing"
    "time"
)

func goprofEnvInt(t *testing.T, key string, fallback int) int {
    t.Helper()
    value := os.Getenv(key)
    if value == "" {
        return fallback
    }
    parsed, err := strconv.Atoi(value)
    if err != nil {
        t.Fatalf("%s: %v", key, err)
    }
    return parsed
}

func goprofOpenLog(t *testing.T, key string) *os.File {
    t.Helper()
    path := os.Getenv(key)
    if path == "" {
        return nil
    }
    handle, err := os.Create(path)
    if err != nil {
        t.Fatalf("%s: %v", key, err)
    }
    t.Cleanup(func() {
        _ = handle.Close()
    })
    return handle
}

func goprofEnvBool(key string) bool {
    value := os.Getenv(key)
    switch value {
    case "1", "true", "TRUE", "yes", "YES", "on", "ON":
        return true
    default:
        return false
    }
}

func goprofTLSMaterial(t *testing.T, sni string) ([]byte, []byte) {
    t.Helper()
    pubKey, key, err := ed25519.GenerateKey(rand.Reader)
    if err != nil {
        t.Fatal(err)
    }
    certPEM, keyPEM, err := newCertificate(nil, nil, sni, pubKey, key)
    if err != nil {
        t.Fatal(err)
    }
    return certPEM, keyPEM
}

func goprofTLSConfig(certPEM, keyPEM, authCertPEM []byte) map[string]any {
    return map[string]any{
        "cert":      string(certPEM),
        "key":       string(keyPEM),
        "authcerts": []string{string(authCertPEM)},
    }
}

func TestProfileIperf3Workload(t *testing.T) {
    iperf3 := os.Getenv("GOPROF_IPERF3")
    if iperf3 == "" {
        iperf3 = "iperf3"
    }
    duration := goprofEnvInt(t, "GOPROF_DURATION", 30)
    parallel := goprofEnvInt(t, "GOPROF_PARALLEL", 10)
    useTLS := goprofEnvBool("GOPROF_TLS")
    muxAddr := os.Getenv("GOPROF_MUX_ADDR")
    clientListenAddr := os.Getenv("GOPROF_CLIENT_LISTEN_ADDR")
    iperfServerAddr := os.Getenv("GOPROF_IPERF_SERVER_ADDR")
    if muxAddr == "" || clientListenAddr == "" || iperfServerAddr == "" {
        t.Fatal("missing workload addresses")
    }

    serverConfig := map[string]any{
        "mux_listen": muxAddr,
        "connect":    iperfServerAddr,
        "max_streams": 1000,
        "mux": map[string]any{
            "tcp":            map[string]any{"nodelay": true, "backlog": 16},
            "max_halfopen":   256,
            "session_window": 16777216,
            "stream_window":  16777216,
        },
    }
    clientConfig := map[string]any{
        "mux_connect": muxAddr,
        "listen":      clientListenAddr,
        "identity":    map[string]any{"claim": "profile-client"},
        "max_streams": 1000,
        "mux": map[string]any{
            "tcp":            map[string]any{"nodelay": true, "backlog": 16},
            "max_halfopen":   256,
            "session_window": 16777216,
            "stream_window":  16777216,
        },
    }
    if useTLS {
        oldServerName := appFlags.ServerName
        appFlags.ServerName = "example.com"
        t.Cleanup(func() {
            appFlags.ServerName = oldServerName
        })

        serverCertPEM, serverKeyPEM := goprofTLSMaterial(t, appFlags.ServerName)
        clientCertPEM, clientKeyPEM := goprofTLSMaterial(t, appFlags.ServerName)
        serverConfig["tls"] = goprofTLSConfig(serverCertPEM, serverKeyPEM, clientCertPEM)
        clientConfig["tls"] = goprofTLSConfig(clientCertPEM, clientKeyPEM, serverCertPEM)
    }

    srv, err := NewServer(newTestConfig(t, serverConfig))
    if err != nil {
        t.Fatal("server create:", err)
    }
    if err := srv.Start(); err != nil {
        t.Fatal("server start:", err)
    }
    t.Cleanup(func() { _ = srv.Shutdown() })

    cli, err := NewServer(newTestConfig(t, clientConfig))
    if err != nil {
        t.Fatal("client create:", err)
    }
    if err := cli.Start(); err != nil {
        t.Fatal("client start:", err)
    }
    t.Cleanup(func() { _ = cli.Shutdown() })

    waitFor(t, 5*time.Second, func() bool { return cli.Stats().NumSessions > 0 })

    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)

    serverStdout := io.Writer(io.Discard)
    serverStderr := io.Writer(io.Discard)
    if handle := goprofOpenLog(t, "GOPROF_IPERF_SERVER_LOG"); handle != nil {
        serverStdout = handle
        serverStderr = handle
    }
    _, serverPort, err := net.SplitHostPort(iperfServerAddr)
    if err != nil {
        t.Fatal(err)
    }
    serverCmd := exec.CommandContext(ctx, iperf3, "-s", "-p", serverPort)
    serverCmd.Stdout = serverStdout
    serverCmd.Stderr = serverStderr
    if err := serverCmd.Start(); err != nil {
        t.Fatal("iperf3 server start:", err)
    }
    t.Cleanup(func() {
        if serverCmd.Process != nil {
            _ = serverCmd.Process.Signal(os.Interrupt)
        }
        _ = serverCmd.Wait()
    })

    time.Sleep(200 * time.Millisecond)

    clientStdout := io.Writer(io.Discard)
    clientStderr := io.Writer(io.Discard)
    if handle := goprofOpenLog(t, "GOPROF_IPERF_STDOUT_LOG"); handle != nil {
        clientStdout = handle
    }
    if handle := goprofOpenLog(t, "GOPROF_IPERF_STDERR_LOG"); handle != nil {
        clientStderr = handle
    }
    host, port, err := net.SplitHostPort(clientListenAddr)
    if err != nil {
        t.Fatal(err)
    }
    clientCmd := exec.CommandContext(
        ctx,
        iperf3,
        "-c", host,
        "-p", port,
        "--bidir",
        "-P", strconv.Itoa(parallel),
        "-t", strconv.Itoa(duration),
    )
    clientCmd.Stdout = clientStdout
    clientCmd.Stderr = clientStderr
    t.Logf("iperf3 workload: %s", clientCmd.String())
    if err := clientCmd.Run(); err != nil {
        t.Fatal("iperf3 client run:", err)
    }
}
'''


@dataclass
class TopRow:
    flat: str
    flat_percent: str
    cumulative: str
    cumulative_percent: str
    name: str


def log(message: str) -> None:
    print(message, file=sys.stderr)


def quote_command(command: Sequence[str]) -> str:
    return " ".join(shlex.quote(part) for part in command)


def ensure_project_root(root: Path) -> None:
    if not (root / "go.mod").exists():
        raise SystemExit("path does not look like a Go module root: %s" % root)


def ensure_tool(name: str) -> str:
    path = shutil.which(name)
    if path is None:
        raise SystemExit("required tool not found: %s" % name)
    return path


def pick_free_tcp_address() -> str:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        host, port = sock.getsockname()
    return "%s:%d" % (host, port)


def resolve_path(base: Path, value: str) -> Path:
    path = Path(value)
    if not path.is_absolute():
        path = base / path
    return path.resolve()


def relative_path(path: Path) -> str:
    return os.path.relpath(path, start=ROOT).replace(os.sep, "/")


def command_timeout_seconds(duration: int, *, grace_seconds: float) -> float:
    return max(1.0, float(duration)) + grace_seconds


def run_command(
        command: Sequence[str],
        *,
        cwd: Optional[Path] = None,
    env: Optional[Dict[str, str]] = None,
        capture_output: bool = False,
        timeout: Optional[float] = None,
) -> subprocess.CompletedProcess[str]:
    log("+ %s" % quote_command(command))
    try:
        return subprocess.run(
            list(command),
            cwd=str(cwd) if cwd is not None else None,
            env=env,
            text=True,
            check=True,
            stdout=subprocess.PIPE if capture_output else None,
            stderr=subprocess.STDOUT if capture_output else None,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise SystemExit("command timed out: %s" % quote_command(command))


def write_profile_test(path: Path) -> None:
    path.write_text(PROFILE_TEST_SOURCE, encoding="utf-8")


def parse_top(text: str) -> List[TopRow]:
    rows: List[TopRow] = []
    in_table = False
    for raw_line in text.splitlines():
        line = raw_line.rstrip()
        if line.startswith("      flat"):
            in_table = True
            continue
        if not in_table:
            continue
        if not line.strip():
            if rows:
                break
            continue
        parts = line.split(None, 5)
        if len(parts) < 6:
            continue
        rows.append(
            TopRow(
                flat=parts[0],
                flat_percent=parts[1],
                cumulative=parts[3],
                cumulative_percent=parts[4],
                name=parts[5],
            )
        )
    return rows


def render_markdown_report(
        *,
        output_path: Path,
        profile_dir: Path,
        test_binary_path: Path,
        cpu_profile_path: Path,
        mem_profile_path: Path,
        test_output_path: Path,
        cpu_top_path: Path,
        mem_top_path: Path,
        cpu_rows: Sequence[TopRow],
        mem_rows: Sequence[TopRow],
        workload_command: str,
        iperf_server_log_path: Path,
        iperf_stdout_log_path: Path,
        iperf_stderr_log_path: Path,
        use_tls: bool,
) -> str:
    output_dir = output_path.parent
    lines = [
        "# Go Profile",
        "",
        "| Field | Value |",
        "| --- | --- |",
        "| Module root | %s |" % relative_path(ROOT),
        "| Profile directory | %s |" % relative_path(profile_dir),
        "| Transport security | %s |" % ("mutual TLS" if use_tls else "plaintext"),
        "| Workload | `%s` |" % workload_command,
        "| Test binary | [%s](%s) |"
        % (
            relative_path(test_binary_path),
            os.path.relpath(test_binary_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| CPU profile | [%s](%s) |"
        % (
            relative_path(cpu_profile_path),
            os.path.relpath(cpu_profile_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| Memory profile | [%s](%s) |"
        % (
            relative_path(mem_profile_path),
            os.path.relpath(mem_profile_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| Test output | [%s](%s) |"
        % (
            relative_path(test_output_path),
            os.path.relpath(test_output_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| iperf3 server log | [%s](%s) |"
        % (
            relative_path(iperf_server_log_path),
            os.path.relpath(iperf_server_log_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| iperf3 stdout | [%s](%s) |"
        % (
            relative_path(iperf_stdout_log_path),
            os.path.relpath(iperf_stdout_log_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| iperf3 stderr | [%s](%s) |"
        % (
            relative_path(iperf_stderr_log_path),
            os.path.relpath(iperf_stderr_log_path, start=output_dir).replace(os.sep, "/"),
        ),
        "",
        "## CPU Hotspots",
        "",
        "| Function | Flat | Flat % | Cum | Cum % |",
        "| --- | ---: | ---: | ---: | ---: |",
    ]
    for row in cpu_rows[:20]:
        lines.append(
            "| %s | %s | %s | %s | %s |"
            % (row.name, row.flat, row.flat_percent, row.cumulative, row.cumulative_percent)
        )
    lines.extend(
        [
            "",
            "## Memory Hotspots",
            "",
            "| Function | Flat | Flat % | Cum | Cum % |",
            "| --- | ---: | ---: | ---: | ---: |",
        ]
    )
    for row in mem_rows[:20]:
        lines.append(
            "| %s | %s | %s | %s | %s |"
            % (row.name, row.flat, row.flat_percent, row.cumulative, row.cumulative_percent)
        )
    lines.extend(
        [
            "",
            "## Raw Reports",
            "",
            "- [CPU top](%s)" % os.path.relpath(cpu_top_path, start=output_dir).replace(os.sep, "/"),
            "- [Memory top](%s)" % os.path.relpath(mem_top_path, start=output_dir).replace(os.sep, "/"),
        ]
    )
    return "\n".join(lines) + "\n"


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run Go profiling and write build/goprof.md."
    )
    parser.add_argument(
        "module_dir",
        help="path to the Go module root, for example v4",
    )
    parser.add_argument("--profile-dir", default=str(DEFAULT_PROFILE_DIR), help="profile output directory (default: ../build/goprof from the script location)")
    parser.add_argument("--output", help="Markdown output path (default: build/goprof.md)")
    parser.add_argument(
        "--iperf3",
        default="iperf3",
        help="iperf3 executable name",
    )
    parser.add_argument(
        "--parallel",
        type=int,
        default=10,
        help="iperf3 parallel stream count (default: 10)",
    )
    parser.add_argument(
        "--duration",
        type=int,
        default=30,
        help="iperf3 test duration in seconds (default: 30)",
    )
    parser.add_argument(
        "--tls",
        action="store_true",
        help="enable mutual TLS on the mux transport (default: off)",
    )
    return parser.parse_args(argv)


def main(argv: Optional[Sequence[str]] = None) -> int:
    global ROOT
    args = parse_args(argv)
    ROOT = resolve_path(Path.cwd().resolve(), args.module_dir)
    ensure_project_root(ROOT)
    ensure_tool("go")
    iperf3 = ensure_tool(args.iperf3)

    profile_dir = resolve_path(ROOT, args.profile_dir)
    output_path = resolve_path(ROOT, args.output) if args.output else DEFAULT_OUTPUT
    profile_dir.mkdir(parents=True, exist_ok=True)

    test_binary_path = profile_dir / "tlswrapper.test"
    cpu_profile_path = profile_dir / "cpu.pprof"
    mem_profile_path = profile_dir / "mem.pprof"
    cpu_top_path = profile_dir / "cpu.top.txt"
    mem_top_path = profile_dir / "mem.top.txt"
    test_output_path = profile_dir / "go-test.log"
    iperf_server_log_path = profile_dir / "iperf3-server.log"
    iperf_stdout_log_path = profile_dir / "iperf3.stdout"
    iperf_stderr_log_path = profile_dir / "iperf3.stderr"
    temp_test_path = ROOT / PROFILE_TEST_FILE
    mux_addr = pick_free_tcp_address()
    client_listen_addr = pick_free_tcp_address()
    iperf_server_addr = pick_free_tcp_address()

    write_profile_test(temp_test_path)
    try:
        run_command(
            ["go", "test", "-mod=vendor", "-c", "-o", str(test_binary_path), "."],
            cwd=ROOT,
            timeout=command_timeout_seconds(args.duration, grace_seconds=BUILD_TIMEOUT_GRACE_SECONDS),
        )
    finally:
        if temp_test_path.exists():
            temp_test_path.unlink()

    test_env = os.environ.copy()
    test_env.update(
        {
            "GOPROF_IPERF3": iperf3,
            "GOPROF_DURATION": str(args.duration),
            "GOPROF_PARALLEL": str(args.parallel),
            "GOPROF_TLS": "1" if args.tls else "0",
            "GOPROF_MUX_ADDR": mux_addr,
            "GOPROF_CLIENT_LISTEN_ADDR": client_listen_addr,
            "GOPROF_IPERF_SERVER_ADDR": iperf_server_addr,
            "GOPROF_IPERF_SERVER_LOG": str(iperf_server_log_path),
            "GOPROF_IPERF_STDOUT_LOG": str(iperf_stdout_log_path),
            "GOPROF_IPERF_STDERR_LOG": str(iperf_stderr_log_path),
        }
    )
    test_proc = run_command(
        [
            str(test_binary_path),
            "-test.run",
            "^%s$" % PROFILE_TEST_NAME,
            "-test.count=1",
            "-test.v",
            "-test.cpuprofile",
            str(cpu_profile_path),
            "-test.memprofile",
            str(mem_profile_path),
        ],
        cwd=ROOT,
        env=test_env,
        capture_output=True,
        timeout=command_timeout_seconds(args.duration, grace_seconds=PROFILE_TIMEOUT_GRACE_SECONDS),
    )
    test_output_path.write_text(test_proc.stdout or "", encoding="utf-8")

    cpu_top_text = run_command(
        ["go", "tool", "pprof", "-top", "-nodecount=25", str(test_binary_path), str(cpu_profile_path)],
        cwd=ROOT,
        capture_output=True,
        timeout=command_timeout_seconds(args.duration, grace_seconds=PPROF_TIMEOUT_GRACE_SECONDS),
    ).stdout or ""
    mem_top_text = run_command(
        [
            "go",
            "tool",
            "pprof",
            "-sample_index=alloc_space",
            "-top",
            "-nodecount=25",
            str(test_binary_path),
            str(mem_profile_path),
        ],
        cwd=ROOT,
        capture_output=True,
        timeout=command_timeout_seconds(args.duration, grace_seconds=PPROF_TIMEOUT_GRACE_SECONDS),
    ).stdout or ""
    cpu_top_path.write_text(cpu_top_text, encoding="utf-8")
    mem_top_path.write_text(mem_top_text, encoding="utf-8")

    report = render_markdown_report(
        output_path=output_path,
        profile_dir=profile_dir,
        test_binary_path=test_binary_path,
        cpu_profile_path=cpu_profile_path,
        mem_profile_path=mem_profile_path,
        test_output_path=test_output_path,
        cpu_top_path=cpu_top_path,
        mem_top_path=mem_top_path,
        cpu_rows=parse_top(cpu_top_text),
        mem_rows=parse_top(mem_top_text),
        workload_command="%s -c 127.0.0.1 -p %s --bidir -P %d -t %d"
        % (iperf3, client_listen_addr.rsplit(":", 1)[1], args.parallel, args.duration),
        iperf_server_log_path=iperf_server_log_path,
        iperf_stdout_log_path=iperf_stdout_log_path,
        iperf_stderr_log_path=iperf_stderr_log_path,
        use_tls=args.tls,
    )
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(report, encoding="utf-8")
    log("wrote %s" % output_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())