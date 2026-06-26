#!/usr/bin/env python3

"""Run a v4 smoke test against the existing build/tlswrapper binary."""

from __future__ import annotations

import argparse
import json
import os
import random
import select
import shlex
import signal
import socket
import subprocess
import sys
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional, Sequence, TextIO, Tuple


SCRIPT_DIR = Path(__file__).resolve().parent
ROOT = SCRIPT_DIR.parent
DEFAULT_BUILD_DIR = ROOT / "build"
CONFIG_TYPE = 'application/x-tlswrapper-config; version="4"'
DEFAULT_DURATION_SECONDS = 10.0
DEFAULT_WORKERS = 16
DEFAULT_STARTUP_TIMEOUT_SECONDS = 10.0
DEFAULT_SHUTDOWN_TIMEOUT_SECONDS = 5.0
DEFAULT_IO_TIMEOUT_SECONDS = 3.0
COMMAND_TIMEOUT_GRACE_SECONDS = 30.0
SERVER_NAME = "example.com"
GENCERT_NAMES = "client,server"
GENCERT_KEYTYPE = "ed25519"
HOST = "127.0.0.1"
PROGRESS_INTERVAL_SECONDS = 1.0
MIN_PAYLOAD_SIZE = 64
MAX_PAYLOAD_SIZE = 4096
WORKER_TARGET_ACTIVE = 3
WORKER_MAX_ACTIVE = 6
SUPPORTED_PROTOCOLS = ("h2mux", "h3mux")
DEFAULT_PROTOCOL = "h2mux"


class SmokeTestError(RuntimeError):
    """Raised when the smoke test cannot complete successfully."""


@dataclass
class RuntimeAssets:
    runtime_dir: Path
    server_config_path: Path
    client_config_path: Path
    server_log_path: Path
    client_log_path: Path
    gencerts_log_path: Path
    api_addr: Tuple[str, int]
    mux_addr: Tuple[str, int]
    server_listen_addr: Tuple[str, int]
    client_listen_addr: Tuple[str, int]
    echo_addr: Tuple[str, int]


@dataclass
class StatsSnapshot:
    opens: int
    open_errors: int
    closes: int
    close_errors: int
    send_ops: int
    recv_ops: int
    io_errors: int
    bytes_sent: int
    bytes_received: int
    active_streams: int
    graceful_eofs: int
    forced_shutdown_closes: int


@dataclass
class SmokeStats:
    opens: int = 0
    open_errors: int = 0
    closes: int = 0
    close_errors: int = 0
    send_ops: int = 0
    recv_ops: int = 0
    io_errors: int = 0
    bytes_sent: int = 0
    bytes_received: int = 0
    active_streams: int = 0
    graceful_eofs: int = 0
    forced_shutdown_closes: int = 0
    _lock: threading.Lock = field(
        default_factory=threading.Lock, init=False, repr=False)

    def record_open(self) -> None:
        with self._lock:
            self.opens += 1
            self.active_streams += 1

    def record_open_error(self) -> None:
        with self._lock:
            self.open_errors += 1

    def record_close(self) -> None:
        with self._lock:
            self.closes += 1
            if self.active_streams > 0:
                self.active_streams -= 1

    def record_close_error(self) -> None:
        with self._lock:
            self.close_errors += 1

    def record_io(self, size: int) -> None:
        with self._lock:
            self.send_ops += 1
            self.recv_ops += 1
            self.bytes_sent += size
            self.bytes_received += size

    def record_io_error(self) -> None:
        with self._lock:
            self.io_errors += 1

    def record_graceful_eof(self) -> None:
        with self._lock:
            self.graceful_eofs += 1
            if self.active_streams > 0:
                self.active_streams -= 1

    def record_forced_shutdown_close(self) -> None:
        with self._lock:
            self.forced_shutdown_closes += 1
            if self.active_streams > 0:
                self.active_streams -= 1

    def snapshot(self) -> StatsSnapshot:
        with self._lock:
            return StatsSnapshot(
                opens=self.opens,
                open_errors=self.open_errors,
                closes=self.closes,
                close_errors=self.close_errors,
                send_ops=self.send_ops,
                recv_ops=self.recv_ops,
                io_errors=self.io_errors,
                bytes_sent=self.bytes_sent,
                bytes_received=self.bytes_received,
                active_streams=self.active_streams,
                graceful_eofs=self.graceful_eofs,
                forced_shutdown_closes=self.forced_shutdown_closes,
            )


@dataclass
class SharedSockets:
    sockets: List[socket.socket] = field(default_factory=list)
    _lock: threading.Lock = field(
        default_factory=threading.Lock, init=False, repr=False)

    def add_many(self, values: List[socket.socket]) -> None:
        if not values:
            return
        with self._lock:
            self.sockets.extend(values)

    def drain(self) -> List[socket.socket]:
        with self._lock:
            drained = list(self.sockets)
            self.sockets = []
        return drained


def log(message: str) -> None:
    print(message, file=sys.stderr, flush=True)


def quote_command(command: Sequence[str]) -> str:
    return " ".join(shlex.quote(part) for part in command)


def command_timeout_seconds(duration_seconds: float) -> float:
    return max(1.0, duration_seconds) + COMMAND_TIMEOUT_GRACE_SECONDS


def ensure_binary(binary_path: Path) -> None:
    if not binary_path.exists():
        raise SmokeTestError("binary not found: %s" % binary_path)
    if not os.access(str(binary_path), os.X_OK):
        raise SmokeTestError("binary is not executable: %s" % binary_path)


def pick_free_port(host: str) -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind((host, 0))
        return int(sock.getsockname()[1])


def make_runtime_dir(build_dir: Path) -> Path:
    name = "smoke-runtime-%d-%d" % (os.getpid(), int(time.time() * 1000))
    return build_dir / name


def build_server_config(assets: RuntimeAssets, protocol: str, window: Optional[int] = None) -> Dict[str, object]:
    mux: Dict[str, object] = {
        "max_streams": 1024,
        "connect_timeout": 5,
        "timeout": 5,
        "keepalive": 5,
        "send_timeout": 5,
        "idle_timeout": 0,
    }
    if window is not None:
        mux["stream_window"] = window
        mux["session_window"] = window
    return {
        "type": CONFIG_TYPE,
        "mux_protocol": protocol,
        "api_listen": "%s:%d" % assets.api_addr,
        "mux_listen": "%s:%d" % assets.mux_addr,
        "listen": "%s:%d" % assets.server_listen_addr,
        "connect": "%s:%d" % assets.echo_addr,
        "identity": {
            "claim": "smoke-server",
        },
        "tls": {
            "cert": "@server-cert.pem",
            "key": "@server-key.pem",
            "authcerts": ["@client-cert.pem"],
        },
        "mux": mux,
        "loglevel": 6,
        "max_sessions": 64,
        "max_startups": "10:30:60",
    }


def build_client_config(assets: RuntimeAssets, protocol: str, window: Optional[int] = None) -> Dict[str, object]:
    mux: Dict[str, object] = {
        "max_streams": 1024,
        "connect_timeout": 5,
        "timeout": 5,
        "keepalive": 5,
        "send_timeout": 5,
        "idle_timeout": 0,
    }
    if window is not None:
        mux["stream_window"] = window
        mux["session_window"] = window
    return {
        "type": CONFIG_TYPE,
        "mux_protocol": protocol,
        "mux_connect": "%s:%d" % assets.mux_addr,
        "listen": "%s:%d" % assets.client_listen_addr,
        "connect": "%s:%d" % assets.echo_addr,
        "identity": {
            "claim": "smoke-client",
        },
        "tls": {
            "cert": "@client-cert.pem",
            "key": "@client-key.pem",
            "authcerts": ["@server-cert.pem"],
        },
        "mux": mux,
        "loglevel": 6,
    }


def write_json(path: Path, payload: Dict[str, object]) -> None:
    path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def run_command(
    command: Sequence[str],
    *,
    cwd: Path,
    timeout_seconds: float,
    log_path: Path,
) -> None:
    log("+ %s" % quote_command(command))
    with log_path.open("w", encoding="utf-8") as handle:
        try:
            subprocess.run(
                list(command),
                cwd=str(cwd),
                stdout=handle,
                stderr=subprocess.STDOUT,
                check=True,
                text=True,
                timeout=timeout_seconds,
            )
        except subprocess.TimeoutExpired:
            raise SmokeTestError("command timed out: %s" %
                                 quote_command(command))
        except subprocess.CalledProcessError as exc:
            raise SmokeTestError(
                "command failed with status %d: %s (see %s)"
                % (exc.returncode, quote_command(command), log_path)
            )


def prepare_runtime_assets(binary_path: Path, build_dir: Path, duration_seconds: float, protocol: str, window: Optional[int] = None) -> RuntimeAssets:
    runtime_dir = make_runtime_dir(build_dir)
    runtime_dir.mkdir(parents=True, exist_ok=False)
    assets = RuntimeAssets(
        runtime_dir=runtime_dir,
        server_config_path=runtime_dir / "server.json",
        client_config_path=runtime_dir / "client.json",
        server_log_path=runtime_dir / "tlswrapper-server.log",
        client_log_path=runtime_dir / "tlswrapper-client.log",
        gencerts_log_path=runtime_dir / "gencerts.log",
        api_addr=(HOST, pick_free_port(HOST)),
        mux_addr=(HOST, pick_free_port(HOST)),
        server_listen_addr=(HOST, pick_free_port(HOST)),
        client_listen_addr=(HOST, pick_free_port(HOST)),
        echo_addr=(HOST, pick_free_port(HOST)),
    )
    run_command(
        [
            str(binary_path),
            "--gencerts",
            GENCERT_NAMES,
            "--sni",
            SERVER_NAME,
            "--keytype",
            GENCERT_KEYTYPE,
        ],
        cwd=runtime_dir,
        timeout_seconds=command_timeout_seconds(duration_seconds),
        log_path=assets.gencerts_log_path,
    )
    write_json(assets.server_config_path, build_server_config(assets, protocol, window))
    write_json(assets.client_config_path, build_client_config(assets, protocol, window))
    return assets


def open_log(path: Path) -> TextIO:
    return path.open("w", encoding="utf-8")


def start_process(command: Sequence[str], cwd: Path, log_path: Path) -> Tuple[subprocess.Popen, TextIO]:
    log("+ %s" % quote_command(command))
    handle = open_log(log_path)
    try:
        proc = subprocess.Popen(
            list(command),
            cwd=str(cwd),
            stdout=handle,
            stderr=subprocess.STDOUT,
            text=True,
        )
    except Exception:
        handle.close()
        raise
    return proc, handle


def terminate_process(proc: subprocess.Popen, name: str) -> None:
    if proc.poll() is not None:
        return
    log("stopping %s [pid:%d]" % (name, proc.pid))
    proc.send_signal(signal.SIGINT)
    try:
        proc.wait(timeout=3.0)
    except subprocess.TimeoutExpired:
        proc.terminate()
        try:
            proc.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=2.0)


def wait_for_port(
    address: Tuple[str, int],
    *,
    timeout_seconds: float,
    proc: Optional[subprocess.Popen] = None,
    name: Optional[str] = None,
    log_path: Optional[Path] = None,
) -> None:
    """Wait until *address* is reachable via TCP, or until *log_path*
    contains a line with the substring \"mux listen\" (which covers UDP /
    QUIC-based mux listeners that have no TCP port to probe).

    When *proc* is given the function also fails fast if that process exits
    before the port or log marker becomes ready."""
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        if proc is not None and proc.poll() is not None:
            raise SmokeTestError(
                "%s exited before becoming ready with status %d (see %s)"
                % (name or "process", proc.returncode, log_path)
            )
        try:
            with socket.create_connection(address, timeout=0.3):
                return
        except OSError:
            pass
        if log_path is not None:
            try:
                text = log_path.read_text(encoding="utf-8")
            except OSError:
                text = ""
            if "mux listen" in text:
                return
        time.sleep(0.05)
    raise SmokeTestError("timed out waiting for %s:%d" % address)


def probe_echo_path(
    address: Tuple[str, int],
    *,
    timeout_seconds: float,
    proc: Optional[subprocess.Popen] = None,
    name: Optional[str] = None,
    log_path: Optional[Path] = None,
) -> None:
    deadline = time.monotonic() + timeout_seconds
    payload = b"smoke-probe"
    while time.monotonic() < deadline:
        if proc is not None and proc.poll() is not None:
            raise SmokeTestError(
                "%s exited before the data path became ready with status %d (see %s)"
                % (name or "process", proc.returncode, log_path)
            )
        try:
            with socket.create_connection(address, timeout=0.5) as sock:
                sock.settimeout(DEFAULT_IO_TIMEOUT_SECONDS)
                sock.sendall(payload)
                received = recv_exact(
                    sock, len(payload), timeout_seconds=DEFAULT_IO_TIMEOUT_SECONDS)
                if received != payload:
                    raise SmokeTestError("probe received mismatched payload")
                return
        except SmokeTestError:
            raise
        except (OSError, EOFError):
            time.sleep(0.05)
    raise SmokeTestError(
        "timed out waiting for end-to-end traffic on %s:%d" % address)


def recv_exact(sock: socket.socket, size: int, timeout_seconds: float) -> bytes:
    sock.settimeout(timeout_seconds)
    chunks = []
    remaining = size
    while remaining > 0:
        chunk = sock.recv(remaining)
        if not chunk:
            raise EOFError("unexpected EOF while reading %d bytes" % size)
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def close_socket(sock: socket.socket) -> None:
    try:
        sock.shutdown(socket.SHUT_RDWR)
    except OSError:
        pass
    try:
        sock.close()
    except OSError:
        pass


def make_connection(address: Tuple[str, int]) -> socket.socket:
    sock = socket.create_connection(
        address, timeout=DEFAULT_IO_TIMEOUT_SECONDS)
    sock.settimeout(DEFAULT_IO_TIMEOUT_SECONDS)
    return sock


def do_echo_roundtrip(sock: socket.socket, payload: bytes) -> None:
    sock.sendall(payload)
    received = recv_exact(sock, len(payload),
                          timeout_seconds=DEFAULT_IO_TIMEOUT_SECONDS)
    if received != payload:
        raise SmokeTestError("echo payload mismatch")


def worker_main(
    worker_id: int,
    seed: int,
    target_addr: Tuple[str, int],
    stop_event: threading.Event,
    stats: SmokeStats,
    lingering: SharedSockets,
) -> None:
    rng = random.Random(seed + worker_id * 100003)
    active = []
    try:
        while not stop_event.is_set():
            action = choose_action(rng, len(active))
            if action == "open":
                try:
                    sock = make_connection(target_addr)
                except OSError:
                    stats.record_open_error()
                else:
                    stats.record_open()
                    active.append(sock)
            elif action == "close":
                index = rng.randrange(len(active))
                sock = active.pop(index)
                try:
                    close_socket(sock)
                except OSError:
                    stats.record_close_error()
                finally:
                    stats.record_close()
            else:
                index = rng.randrange(len(active))
                sock = active[index]
                payload_size = rng.randint(MIN_PAYLOAD_SIZE, MAX_PAYLOAD_SIZE)
                payload = os.urandom(payload_size)
                try:
                    do_echo_roundtrip(sock, payload)
                except (OSError, EOFError, SmokeTestError):
                    stats.record_io_error()
                    try:
                        close_socket(sock)
                    finally:
                        active.pop(index)
                        stats.record_close()
                else:
                    stats.record_io(payload_size)
            time.sleep(rng.uniform(0.01, 0.06))
    finally:
        lingering.add_many(active)


def choose_action(rng: random.Random, active_count: int) -> str:
    if active_count == 0:
        return "open"
    if active_count < WORKER_TARGET_ACTIVE:
        return "open" if rng.random() < 0.45 else "io"
    roll = rng.random()
    if active_count < WORKER_MAX_ACTIVE and roll < 0.20:
        return "open"
    if active_count > 1 and roll < 0.40:
        return "close"
    return "io"


def ensure_lingering_connections(
    sockets_left_open: List[socket.socket],
    target_addr: Tuple[str, int],
    stats: SmokeStats,
    count: int,
) -> None:
    while len(sockets_left_open) < count:
        sock = None
        try:
            sock = make_connection(target_addr)
            stats.record_open()
            payload = os.urandom(256)
            do_echo_roundtrip(sock, payload)
            stats.record_io(len(payload))
            sockets_left_open.append(sock)
        except (OSError, EOFError, SmokeTestError):
            if sock is None:
                stats.record_open_error()
            else:
                stats.record_io_error()
                close_socket(sock)
            break


def wait_for_graceful_close(
    sockets_left_open: List[socket.socket],
    stats: SmokeStats,
    timeout_seconds: float,
) -> int:
    remaining = list(sockets_left_open)
    closed = 0
    deadline = time.monotonic() + timeout_seconds
    peek_flag = getattr(socket, "MSG_PEEK", 0)
    while remaining and time.monotonic() < deadline:
        wait_seconds = min(0.2, max(0.0, deadline - time.monotonic()))
        readable, _, exceptional = select.select(
            remaining, [], remaining, wait_seconds)
        for sock in list(set(readable + exceptional)):
            try:
                data = sock.recv(1, peek_flag)
            except OSError:
                data = b""
            if data == b"":
                close_socket(sock)
                remaining.remove(sock)
                closed += 1
                stats.record_graceful_eof()
    for sock in remaining:
        close_socket(sock)
        stats.record_forced_shutdown_close()
    return closed


def wait_for_process_exit(proc: subprocess.Popen, name: str, timeout_seconds: float, log_path: Path) -> int:
    try:
        return int(proc.wait(timeout=timeout_seconds))
    except subprocess.TimeoutExpired:
        terminate_process(proc, name)
        if proc.poll() is None:
            raise SmokeTestError("%s did not exit within %.1fs (see %s)" % (
                name, timeout_seconds, log_path))
        return int(proc.returncode)


def format_snapshot(snapshot: StatsSnapshot) -> str:
    return (
        "opens=%d open_errors=%d sends=%d recvs=%d closes=%d io_errors=%d "
        "active=%d bytes_sent=%d bytes_received=%d"
        % (
            snapshot.opens,
            snapshot.open_errors,
            snapshot.send_ops,
            snapshot.recv_ops,
            snapshot.closes,
            snapshot.io_errors,
            snapshot.active_streams,
            snapshot.bytes_sent,
            snapshot.bytes_received,
        )
    )


def evaluate(
    snapshot: StatsSnapshot,
    *,
    server_exit_code: int,
    client_exit_code: int,
    graceful_shutdown_seconds: float,
    graceful_closed: int,
) -> List[str]:
    failures = []
    if snapshot.opens <= 0:
        failures.append("no streams were opened")
    if snapshot.send_ops <= 0 or snapshot.recv_ops <= 0:
        failures.append("send/receive operations did not complete")
    if snapshot.closes <= 0:
        failures.append("no streams were closed during workload")
    if graceful_closed <= 0:
        failures.append("no lingering streams were closed by server shutdown")
    if snapshot.io_errors > max(5, snapshot.opens // 3):
        failures.append("too many I/O errors: %d" % snapshot.io_errors)
    if server_exit_code != 0:
        failures.append("server exit code was %d" % server_exit_code)
    if client_exit_code != 0:
        failures.append("client exit code was %d" % client_exit_code)
    if graceful_shutdown_seconds > DEFAULT_SHUTDOWN_TIMEOUT_SECONDS:
        failures.append(
            "graceful shutdown took %.2fs, expected <= %.2fs"
            % (graceful_shutdown_seconds, DEFAULT_SHUTDOWN_TIMEOUT_SECONDS)
        )
    return failures


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--build-dir",
        default=str(DEFAULT_BUILD_DIR),
        help="directory containing the existing tlswrapper build (default: %(default)s)",
    )
    parser.add_argument(
        "--duration",
        type=float,
        default=DEFAULT_DURATION_SECONDS,
        help="random workload duration in seconds (default: %(default)s)",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=DEFAULT_WORKERS,
        help="number of concurrent workload workers (default: %(default)s)",
    )
    parser.add_argument(
        "--seed",
        type=int,
        default=None,
        help="random seed for reproducible workloads (default: current time)",
    )
    parser.add_argument(
        "--protocol",
        "-p",
        choices=SUPPORTED_PROTOCOLS,
        default=DEFAULT_PROTOCOL,
        help="mux transport protocol (default: %(default)s)",
    )
    parser.add_argument(
        "--window",
        type=int,
        default=None,
        help="set both mux stream_window and session_window in bytes (default: omit, use Go defaults)",
    )
    return parser.parse_args(argv)


def run_smoke_test(args: argparse.Namespace) -> int:
    build_dir = Path(args.build_dir).expanduser().resolve()
    binary_path = build_dir / "tlswrapper"
    ensure_binary(binary_path)
    if args.duration <= 0:
        raise SmokeTestError("duration must be positive")
    if args.workers <= 0:
        raise SmokeTestError("workers must be positive")

    seed = int(args.seed if args.seed is not None else time.time() * 1000)
    log("seed=%d" % seed)

    assets = None
    stats = SmokeStats()
    lingering = SharedSockets()
    stop_event = threading.Event()
    echo_stop = threading.Event()
    server_proc = None
    client_proc = None
    server_log_handle = None
    client_log_handle = None
    workers = []
    echo_thread = None
    graceful_closed = 0
    graceful_shutdown_seconds = 0.0
    server_exit_code = -1
    client_exit_code = -1

    try:
        assets = prepare_runtime_assets(binary_path, build_dir, args.duration, args.protocol, args.window)
        echo_thread = threading.Thread(
            target=run_echo_server,
            args=(assets.echo_addr, echo_stop),
            name="smoke-echo-server",
            daemon=True,
        )
        echo_thread.start()
        wait_for_port(assets.echo_addr,
                      timeout_seconds=DEFAULT_STARTUP_TIMEOUT_SECONDS)

        server_proc, server_log_handle = start_process(
            [str(binary_path), "-c", str(assets.server_config_path)],
            cwd=assets.runtime_dir,
            log_path=assets.server_log_path,
        )
        wait_for_port(
            assets.mux_addr,
            timeout_seconds=DEFAULT_STARTUP_TIMEOUT_SECONDS,
            proc=server_proc,
            name="tlswrapper server",
            log_path=assets.server_log_path,
        )

        client_proc, client_log_handle = start_process(
            [str(binary_path), "-c", str(assets.client_config_path)],
            cwd=assets.runtime_dir,
            log_path=assets.client_log_path,
        )
        wait_for_port(
            assets.client_listen_addr,
            timeout_seconds=DEFAULT_STARTUP_TIMEOUT_SECONDS,
            proc=client_proc,
            name="tlswrapper client",
            log_path=assets.client_log_path,
        )
        probe_echo_path(
            assets.client_listen_addr,
            timeout_seconds=DEFAULT_STARTUP_TIMEOUT_SECONDS,
            proc=client_proc,
            name="tlswrapper client",
            log_path=assets.client_log_path,
        )

        for worker_id in range(args.workers):
            thread = threading.Thread(
                target=worker_main,
                args=(worker_id, seed, assets.client_listen_addr,
                      stop_event, stats, lingering),
                name="smoke-worker-%02d" % worker_id,
                daemon=True,
            )
            thread.start()
            workers.append(thread)

        deadline = time.monotonic() + args.duration
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            time.sleep(min(PROGRESS_INTERVAL_SECONDS, remaining))
            snapshot = stats.snapshot()
            log(format_snapshot(snapshot))

        stop_event.set()
        for thread in workers:
            thread.join(timeout=2.0)

        sockets_left_open = lingering.drain()
        ensure_lingering_connections(
            sockets_left_open, assets.client_listen_addr, stats, count=2)

        shutdown_start = time.monotonic()
        log("sending SIGTERM to tlswrapper server [pid:%d]" % server_proc.pid)
        server_proc.send_signal(signal.SIGTERM)
        graceful_closed = wait_for_graceful_close(
            sockets_left_open,
            stats,
            timeout_seconds=DEFAULT_SHUTDOWN_TIMEOUT_SECONDS,
        )
        server_exit_code = wait_for_process_exit(
            server_proc,
            "tlswrapper server",
            DEFAULT_SHUTDOWN_TIMEOUT_SECONDS,
            assets.server_log_path,
        )
        graceful_shutdown_seconds = time.monotonic() - shutdown_start

        terminate_process(client_proc, "tlswrapper client")
        client_exit_code = int(client_proc.wait(timeout=3.0))

        snapshot = stats.snapshot()
        failures = evaluate(
            snapshot,
            server_exit_code=server_exit_code,
            client_exit_code=client_exit_code,
            graceful_shutdown_seconds=graceful_shutdown_seconds,
            graceful_closed=graceful_closed,
        )

        if failures:
            print("FAIL")
            print("seed=%d" % seed)
            print("runtime_dir=%s" % assets.runtime_dir)
            print(format_snapshot(snapshot))
            print("graceful_closed=%d" % graceful_closed)
            print("graceful_shutdown_seconds=%.3f" % graceful_shutdown_seconds)
            print("server_exit_code=%d" % server_exit_code)
            print("client_exit_code=%d" % client_exit_code)
            for failure in failures:
                print("reason=%s" % failure)
            return 1

        print("PASS")
        print("seed=%d" % seed)
        print("runtime_dir=%s" % assets.runtime_dir)
        print(format_snapshot(snapshot))
        print("graceful_closed=%d" % graceful_closed)
        print("graceful_shutdown_seconds=%.3f" % graceful_shutdown_seconds)
        print("server_exit_code=%d" % server_exit_code)
        print("client_exit_code=%d" % client_exit_code)
        return 0
    finally:
        stop_event.set()
        echo_stop.set()
        if client_proc is not None:
            try:
                terminate_process(client_proc, "tlswrapper client")
            except Exception:
                pass
        if server_proc is not None:
            try:
                terminate_process(server_proc, "tlswrapper server")
            except Exception:
                pass
        if server_log_handle is not None:
            server_log_handle.close()
        if client_log_handle is not None:
            client_log_handle.close()
        if echo_thread is not None:
            echo_thread.join(timeout=1.0)
        for sock in lingering.drain():
            close_socket(sock)


def run_echo_server(address: Tuple[str, int], stop_event: threading.Event) -> None:
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(address)
    listener.listen(64)
    listener.settimeout(0.5)
    try:
        while not stop_event.is_set():
            try:
                conn, _ = listener.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            thread = threading.Thread(
                target=echo_connection,
                args=(conn, stop_event),
                name="smoke-echo-conn",
                daemon=True,
            )
            thread.start()
    finally:
        close_socket(listener)


def echo_connection(conn: socket.socket, stop_event: threading.Event) -> None:
    conn.settimeout(0.5)
    try:
        while not stop_event.is_set():
            try:
                data = conn.recv(8192)
            except socket.timeout:
                continue
            except OSError:
                break
            if not data:
                break
            try:
                conn.sendall(data)
            except OSError:
                break
    finally:
        close_socket(conn)


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = parse_args(argv)
    try:
        return run_smoke_test(args)
    except KeyboardInterrupt:
        log("interrupted")
        return 130
    except SmokeTestError as exc:
        print("FAIL")
        print("reason=%s" % exc)
        return 1


if __name__ == "__main__":
    sys.exit(main())
