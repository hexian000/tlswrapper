#!/usr/bin/env python3

"""Run Go-native profiling for a tlswrapper module and write a Markdown report."""

from __future__ import annotations

import argparse
import os
import shlex
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import List, Optional, Sequence


SCRIPT_DIR = Path(__file__).resolve().parent
ROOT = Path.cwd().resolve()
DEFAULT_BUILD_DIR = SCRIPT_DIR.parent / "build"
DEFAULT_PROFILE_DIR = DEFAULT_BUILD_DIR / "goprof"
DEFAULT_OUTPUT = DEFAULT_BUILD_DIR / "goprof.md"


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


def resolve_path(base: Path, value: str) -> Path:
    path = Path(value)
    if not path.is_absolute():
        path = base / path
    return path.resolve()


def relative_path(path: Path) -> str:
    return os.path.relpath(path, start=ROOT).replace(os.sep, "/")


def run_command(
        command: Sequence[str],
        *,
        cwd: Optional[Path] = None,
        capture_output: bool = False,
) -> subprocess.CompletedProcess[str]:
    log("+ %s" % quote_command(command))
    return subprocess.run(
        list(command),
        cwd=str(cwd) if cwd is not None else None,
        text=True,
        check=True,
        stdout=subprocess.PIPE if capture_output else None,
        stderr=subprocess.STDOUT if capture_output else None,
    )


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
        test_name: str,
        test_count: int,
) -> str:
    output_dir = output_path.parent
    lines = [
        "# Go Profile",
        "",
        "| Field | Value |",
        "| --- | --- |",
        "| Module root | %s |" % relative_path(ROOT),
        "| Profile directory | %s |" % relative_path(profile_dir),
        "| Test workload | %s |" % test_name,
        "| Repetitions | %d |" % test_count,
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
        "--test",
        default="TestForwardBidirectional",
        help="root-package test used as the profiling workload (default: TestForwardBidirectional)",
    )
    parser.add_argument(
        "--count",
        type=int,
        default=50,
        help="number of times to repeat the profiling workload (default: 50)",
    )
    return parser.parse_args(argv)


def main(argv: Optional[Sequence[str]] = None) -> int:
    global ROOT
    args = parse_args(argv)
    ROOT = resolve_path(Path.cwd().resolve(), args.module_dir)
    ensure_project_root(ROOT)
    ensure_tool("go")

    profile_dir = resolve_path(ROOT, args.profile_dir)
    output_path = resolve_path(ROOT, args.output) if args.output else DEFAULT_OUTPUT
    profile_dir.mkdir(parents=True, exist_ok=True)

    test_binary_path = profile_dir / "tlswrapper.test"
    cpu_profile_path = profile_dir / "cpu.pprof"
    mem_profile_path = profile_dir / "mem.pprof"
    cpu_top_path = profile_dir / "cpu.top.txt"
    mem_top_path = profile_dir / "mem.top.txt"
    test_output_path = profile_dir / "go-test.log"

    run_command(
        ["go", "test", "-mod=vendor", "-c", "-o", str(test_binary_path), "."],
        cwd=ROOT,
    )
    test_proc = run_command(
        [
            str(test_binary_path),
            "-test.run",
            "^%s$" % args.test,
            "-test.count=%d" % args.count,
            "-test.v",
            "-test.cpuprofile",
            str(cpu_profile_path),
            "-test.memprofile",
            str(mem_profile_path),
        ],
        cwd=ROOT,
        capture_output=True,
    )
    test_output_path.write_text(test_proc.stdout or "", encoding="utf-8")

    cpu_top_text = run_command(
        ["go", "tool", "pprof", "-top", "-nodecount=25", str(test_binary_path), str(cpu_profile_path)],
        cwd=ROOT,
        capture_output=True,
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
        test_name=args.test,
        test_count=args.count,
    )
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(report, encoding="utf-8")
    log("wrote %s" % output_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())