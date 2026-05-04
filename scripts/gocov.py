#!/usr/bin/env python3

"""Run Go coverage for a tlswrapper module and write a Markdown report."""

from __future__ import annotations

import argparse
import os
import re
import shlex
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Tuple


SCRIPT_DIR = Path(__file__).resolve().parent
ROOT = Path.cwd().resolve()
DEFAULT_BUILD_DIR = SCRIPT_DIR.parent / "build"
DEFAULT_OUTPUT = DEFAULT_BUILD_DIR / "gocov.md"
DEFAULT_PROFILE_DIR = DEFAULT_BUILD_DIR / "gocov"
DEFAULT_LINE_DIR = DEFAULT_PROFILE_DIR / "line-coverage"
EXCLUDED_SUFFIXES = ("_test.go", ".pb.go", "_grpc.pb.go")
FUNCTION_LINE_RE = re.compile(r"^(?P<file>.+?\.go):(?P<line>[0-9]+):\s+(?P<func>\S+)\s+(?P<pct>[0-9.]+%)$")


@dataclass
class FileCoverage:
    executable_lines: set[int] = field(default_factory=set)
    covered_lines: set[int] = field(default_factory=set)
    line_hits: Dict[int, int] = field(default_factory=dict)
    covered_statements: int = 0
    total_statements: int = 0

    @property
    def covered(self) -> int:
        return len(self.covered_lines)

    @property
    def total(self) -> int:
        return len(self.executable_lines)

    @property
    def percent(self) -> float:
        if self.total == 0:
            return 0.0
        return 100.0 * float(self.covered) / float(self.total)

    @property
    def statement_percent(self) -> float:
        if self.total_statements == 0:
            return 0.0
        return 100.0 * float(self.covered_statements) / float(self.total_statements)


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


def module_path(root: Path) -> str:
    go_mod = root / "go.mod"
    for raw_line in go_mod.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if line.startswith("module "):
            return line[len("module "):].strip()
    raise SystemExit("failed to determine module path from %s" % go_mod)


def is_tracked_source(path_text: str) -> bool:
    return not any(path_text.endswith(suffix) for suffix in EXCLUDED_SUFFIXES)


def normalize_source_path(path_text: str, module_name: str) -> Optional[str]:
    if path_text.startswith(module_name + "/"):
        path_text = path_text[len(module_name) + 1:]
    source_path = Path(path_text)
    if source_path.is_absolute():
        try:
            source_path = source_path.resolve().relative_to(ROOT)
        except ValueError:
            return None
    normalized = source_path.as_posix()
    if normalized.startswith("./"):
        normalized = normalized[2:]
    if normalized.startswith("vendor/") or not is_tracked_source(normalized):
        return None
    return normalized


def parse_profile_line(line: str, module_name: str) -> tuple[Optional[str], Optional[tuple[int, int, int, int, int, int]]]:
    source_part, _, rest = line.partition(":")
    if not rest:
        return None, None
    normalized = normalize_source_path(source_part, module_name)
    if normalized is None:
        return None, None
    span_text, _, counts_text = rest.partition(" ")
    parts = counts_text.split()
    if len(parts) != 2:
        return None, None
    start_text, _, end_text = span_text.partition(",")
    start_line_text, _, start_col_text = start_text.partition(".")
    end_line_text, _, end_col_text = end_text.partition(".")
    return normalized, (
        int(start_line_text),
        int(start_col_text),
        int(end_line_text),
        int(end_col_text),
        int(parts[0]),
        int(parts[1]),
    )


def collect_coverage(profile_path: Path, module_name: str) -> Dict[str, FileCoverage]:
    coverage: Dict[str, FileCoverage] = {}
    with profile_path.open("r", encoding="utf-8") as handle:
        for raw_line in handle:
            line = raw_line.strip()
            if not line or line.startswith("mode:"):
                continue
            source_rel, data = parse_profile_line(line, module_name)
            if source_rel is None or data is None:
                continue
            start_line, _start_col, end_line, _end_col, num_statements, count = data
            file_coverage = coverage.setdefault(source_rel, FileCoverage())
            for line_number in range(start_line, end_line + 1):
                file_coverage.executable_lines.add(line_number)
                if count > 0:
                    file_coverage.covered_lines.add(line_number)
                    file_coverage.line_hits[line_number] = file_coverage.line_hits.get(line_number, 0) + count
            file_coverage.total_statements += num_statements
            if count > 0:
                file_coverage.covered_statements += num_statements
    return coverage


def iter_expected_sources() -> List[str]:
    sources: List[str] = []
    for path in sorted(ROOT.rglob("*.go")):
        if "vendor" in path.parts:
            continue
        rel = path.relative_to(ROOT).as_posix()
        if not is_tracked_source(rel):
            continue
        sources.append(rel)
    return sources


def summarize_rows(expected_sources: Iterable[str], coverage: Dict[str, FileCoverage]) -> List[Tuple[str, FileCoverage]]:
    rows: List[Tuple[str, FileCoverage]] = []
    for source in expected_sources:
        rows.append((source, coverage.get(source, FileCoverage())))
    return rows


def clear_dir(path: Path) -> None:
    if path.exists():
        shutil.rmtree(path)
    path.mkdir(parents=True, exist_ok=True)


def write_line_files(line_dir: Path, rows: Sequence[Tuple[str, FileCoverage]]) -> None:
    clear_dir(line_dir)
    for source, file_coverage in rows:
        source_path = ROOT / source
        if not source_path.exists():
            continue
        output_path = line_dir / (source + ".cover")
        output_path.parent.mkdir(parents=True, exist_ok=True)
        source_lines = source_path.read_text(encoding="utf-8", errors="replace").splitlines()
        with output_path.open("w", encoding="utf-8") as handle:
            handle.write("        -:    0:Source:%s\n" % source)
            handle.write("        -:    0:Generator:gocov.py\n")
            handle.write(
                "        -:    0:Lines executed:%.2f%% of %d\n"
                % (file_coverage.percent, file_coverage.total)
            )
            for line_number, source_line in enumerate(source_lines, start=1):
                if line_number not in file_coverage.executable_lines:
                    count_token = "-"
                else:
                    count = file_coverage.line_hits.get(line_number, 0)
                    count_token = str(count) if count > 0 else "#####"
                handle.write("%9s:%5d:%s\n" % (count_token, line_number, source_line))


def classify_scope(source_rel: str) -> str:
    parts = Path(source_rel).parts
    if len(parts) <= 1:
        return "root"
    return parts[0]


def build_summary_rows(rows: Sequence[Tuple[str, FileCoverage]]) -> List[Tuple[str, int, int, int, int]]:
    summary: Dict[str, List[int]] = {}
    overall = [0, 0, 0, 0]
    for source, file_coverage in rows:
        bucket = summary.setdefault(classify_scope(source), [0, 0, 0, 0])
        bucket[0] += file_coverage.covered
        bucket[1] += file_coverage.total
        bucket[2] += file_coverage.covered_statements
        bucket[3] += file_coverage.total_statements
        overall[0] += file_coverage.covered
        overall[1] += file_coverage.total
        overall[2] += file_coverage.covered_statements
        overall[3] += file_coverage.total_statements
    rows_out = [
        (label, counts[0], counts[1], counts[2], counts[3])
        for label, counts in sorted(summary.items())
    ]
    rows_out.append(("overall", overall[0], overall[1], overall[2], overall[3]))
    return rows_out


def format_percent(covered: int, total: int) -> str:
    if total == 0:
        return "0.00%"
    return "%.2f%%" % (100.0 * float(covered) / float(total))


def parse_function_summary(text: str, module_name: str) -> tuple[str, List[Tuple[str, str, float]]]:
    total_percent = "0.00%"
    rows: List[Tuple[str, str, float]] = []
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        if line.startswith("total:"):
            total_percent = line.rsplit(None, 1)[-1]
            continue
        match = FUNCTION_LINE_RE.match(line)
        if match is None:
            continue
        source_rel = normalize_source_path(match.group("file"), module_name)
        if source_rel is None:
            continue
        function_name = match.group("func")
        percent_text = match.group("pct")
        try:
            percent_value = float(percent_text.rstrip("%"))
        except ValueError:
            continue
        rows.append(("%s:%s" % (source_rel, function_name), percent_text, percent_value))
    rows.sort(key=lambda item: (item[2], item[0]))
    return total_percent, rows


def render_markdown_report(
        rows: Sequence[Tuple[str, FileCoverage]],
        *,
        output_path: Path,
        line_dir: Path,
        profile_path: Path,
        function_text_path: Path,
        function_total: str,
        function_rows: Sequence[Tuple[str, str, float]],
) -> str:
    output_dir = output_path.parent
    summary_rows = build_summary_rows(rows)
    lines = [
        "# Go Coverage",
        "",
        "| Field | Value |",
        "| --- | --- |",
        "| Module root | %s |" % relative_path(ROOT),
        "| Coverage profile | [%s](%s) |"
        % (
            relative_path(profile_path),
            os.path.relpath(profile_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| Function summary | [%s](%s) |"
        % (
            relative_path(function_text_path),
            os.path.relpath(function_text_path, start=output_dir).replace(os.sep, "/"),
        ),
        "| Total statement coverage | %s |" % function_total,
        "",
        "## Summary",
        "",
        "| Scope | Covered Lines | Total Lines | Line % | Covered Statements | Total Statements | Statement % |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for label, covered, total, stmt_covered, stmt_total in summary_rows:
        lines.append(
            "| %s | %d | %d | %s | %d | %d | %s |"
            % (
                label,
                covered,
                total,
                format_percent(covered, total),
                stmt_covered,
                stmt_total,
                format_percent(stmt_covered, stmt_total),
            )
        )
    lines.extend(
        [
            "",
            "## Lowest Covered Functions",
            "",
            "| Function | Coverage |",
            "| --- | ---: |",
        ]
    )
    for function_name, percent_text, _percent_value in function_rows[:20]:
        lines.append("| %s | %s |" % (function_name, percent_text))
    lines.extend(
        [
            "",
            "## Files",
            "",
            "| File | Covered Lines | Total Lines | Line % | Statement % | Line Data |",
            "| --- | ---: | ---: | ---: | ---: | --- |",
        ]
    )
    for source, file_coverage in sorted(rows, key=lambda item: (item[1].percent, item[0])):
        line_file = line_dir / (source + ".cover")
        lines.append(
            "| %s | %d | %d | %.2f%% | %.2f%% | [cover](%s) |"
            % (
                source,
                file_coverage.covered,
                file_coverage.total,
                file_coverage.percent,
                file_coverage.statement_percent,
                os.path.relpath(line_file, start=output_dir).replace(os.sep, "/"),
            )
        )
    return "\n".join(lines) + "\n"


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run Go coverage and write build/gocov.md."
    )
    parser.add_argument(
        "module_dir",
        help="path to the Go module root, for example v4",
    )
    parser.add_argument("--build-dir", default=str(DEFAULT_BUILD_DIR), help="build directory (default: ../build from the script location)")
    parser.add_argument("--profile-dir", default=str(DEFAULT_PROFILE_DIR), help="coverage output directory (default: ../build/gocov from the script location)")
    parser.add_argument("--output", help="Markdown output path (default: build/gocov.md)")
    parser.add_argument(
        "--packages",
        default="./...",
        help="package pattern passed to go test (default: ./...)",
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
    profile_path = profile_dir / "coverage.out"
    function_text_path = profile_dir / "coverage.func.txt"
    test_log_path = profile_dir / "go-test.log"
    module_name = module_path(ROOT)

    test_proc = run_command(
        [
            "go",
            "test",
            "-mod=vendor",
            "-count=1",
            "-covermode=count",
            "-coverprofile",
            str(profile_path),
            args.packages,
        ],
        cwd=ROOT,
        capture_output=True,
    )
    test_log_path.write_text(test_proc.stdout or "", encoding="utf-8")

    func_proc = run_command(
        ["go", "tool", "cover", "-func", str(profile_path)],
        cwd=ROOT,
        capture_output=True,
    )
    function_text = func_proc.stdout or ""
    function_text_path.write_text(function_text, encoding="utf-8")

    coverage = collect_coverage(profile_path, module_name)
    rows = summarize_rows(iter_expected_sources(), coverage)
    write_line_files(DEFAULT_LINE_DIR if profile_dir == DEFAULT_PROFILE_DIR else profile_dir / "line-coverage", rows)
    line_dir = DEFAULT_LINE_DIR if profile_dir == DEFAULT_PROFILE_DIR else profile_dir / "line-coverage"
    function_total, function_rows = parse_function_summary(function_text, module_name)
    report = render_markdown_report(
        rows,
        output_path=output_path,
        line_dir=line_dir,
        profile_path=profile_path,
        function_text_path=function_text_path,
        function_total=function_total,
        function_rows=function_rows,
    )
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(report, encoding="utf-8")
    log("wrote %s" % output_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())