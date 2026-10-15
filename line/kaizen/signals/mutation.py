"""Mutation score of the code a kaizen change is about to touch.

Unlike the signals of measure.py this is not computed over the whole repo: a
mutation run takes minutes, so it covers one scope (a Go package directory or
a list of Python/TypeScript files) under a wall-time cap. When the cap is hit,
the tool is missing, or its output has no mutants, the score is Unavailable
with the reason, never 0 (kaizen.md section 1.1 treats a low score as "write
characterization tests first"; a missing score must not look like one).

The tools are established ones, pinned in the station image: gremlins (Go),
mutmut 2.x (Python), Stryker (TypeScript).
"""

import json
import os
import re
import subprocess
import tempfile
import time
from collections.abc import Callable
from dataclasses import dataclass

from adapter import Unavailable

DEFAULT_CAP_SECONDS = 600

# (argv, cwd, timeout seconds) -> stdout. Raises subprocess.TimeoutExpired when the
# cap is hit and FileNotFoundError when the tool is not installed.
Runner = Callable[[list[str], str, float], str]


@dataclass(frozen=True)
class MutantCounts:
    killed: int
    survived: int  # includes mutants in code no test reaches: nothing detects them
    timed_out: int  # a mutant that hangs the tests was detected


@dataclass(frozen=True)
class MutationTool:
    language: str
    name: str
    version_argv: tuple[str, ...]
    command: Callable[[tuple[str, ...], str], list[str]]  # (scope, report dir) -> argv
    counts: Callable[[str, str], MutantCounts]  # (stdout, report dir) -> counts


def _gremlins_command(scope: tuple[str, ...], report_dir: str) -> list[str]:
    return ["gremlins", "unleash", "--output", os.path.join(report_dir, "gremlins.json"), *scope]


def _gremlins_counts(stdout: str, report_dir: str) -> MutantCounts:
    with open(os.path.join(report_dir, "gremlins.json"), encoding="utf-8") as f:
        report = json.load(f)
    statuses = [m["status"] for file in report.get("files", []) for m in file["mutations"]]
    return MutantCounts(
        killed=statuses.count("KILLED"),
        survived=statuses.count("LIVED") + statuses.count("NOT COVERED"),
        timed_out=statuses.count("TIMED OUT"),
    )


def _mutmut_command(scope: tuple[str, ...], report_dir: str) -> list[str]:
    return ["mutmut", "run", "--paths-to-mutate", ",".join(scope)]


def _mutmut_counts(stdout: str, report_dir: str) -> MutantCounts:
    """Reads the last progress line: '⠏ 12/12  🎉 10 ⏰ 1  🤔 0  🙁 1  🔇 0'."""
    progress = [line for line in stdout.splitlines() if "🎉" in line]
    if not progress:
        raise ValueError("mutmut printed no progress line")
    found = {emoji: int(n) for emoji, n in re.findall(r"(🎉|⏰|🙁|🤔)\s*(\d+)", progress[-1])}
    return MutantCounts(killed=found["🎉"], survived=found["🙁"] + found["🤔"], timed_out=found["⏰"])


def _stryker_command(scope: tuple[str, ...], report_dir: str) -> list[str]:
    return ["npx", "stryker", "run", "--mutate", ",".join(scope),
            "--reporters", "json", "--jsonReporter.fileName", os.path.join(report_dir, "stryker.json")]


def _stryker_counts(stdout: str, report_dir: str) -> MutantCounts:
    with open(os.path.join(report_dir, "stryker.json"), encoding="utf-8") as f:
        report = json.load(f)
    statuses = [m["status"] for file in report["files"].values() for m in file["mutants"]]
    return MutantCounts(
        killed=statuses.count("Killed"),
        survived=statuses.count("Survived") + statuses.count("NoCoverage"),
        timed_out=statuses.count("Timeout"),
    )


# A literal table so search finds each tool; adding a language is adding a line.
TOOLS: dict[str, MutationTool] = {
    "go": MutationTool("go", "gremlins", ("gremlins", "version"), _gremlins_command, _gremlins_counts),
    "python": MutationTool("python", "mutmut", ("mutmut", "--version"), _mutmut_command, _mutmut_counts),
    "typescript": MutationTool(
        "typescript", "stryker", ("npx", "stryker", "--version"), _stryker_command, _stryker_counts),
}


def run_subprocess(argv: list[str], cwd: str, timeout: float) -> str:
    # Mutation tools exit non-zero when mutants survive, so the exit code is not a failure.
    return subprocess.run(argv, cwd=cwd, capture_output=True, text=True, timeout=timeout).stdout


def mutation_signals(
    root: str, language: str, scope: tuple[str, ...], cap_seconds: float = DEFAULT_CAP_SECONDS,
    run: Runner = run_subprocess,
) -> dict:
    """The mutation signals of `scope` (paths relative to root): the tool and
    its version, killed/survived/timed_out, the score in [0, 1] (higher is
    better) and its sample_size (mutants that counted), or Unavailable for
    every count when the run could not finish. duration_ms is the wall time of
    the whole call either way: the line records it to learn whether running
    mutation on every card is affordable."""
    tool = TOOLS[language]
    started = time.monotonic()
    result: dict = {"language": language, "scope": list(scope), "tool": tool.name}

    def elapsed_ms() -> int:
        return round((time.monotonic() - started) * 1000)

    def unavailable(reason: str) -> dict:
        gone = Unavailable(reason)
        return {**result, "tool_version": gone, "killed": gone, "survived": gone, "timed_out": gone,
                "mutation_score": gone, "sample_size": gone, "duration_ms": elapsed_ms()}

    try:
        version = run(list(tool.version_argv), root, cap_seconds).strip()
    except FileNotFoundError:
        return unavailable(f"{tool.name} is not installed")
    result["tool_version"] = version
    with tempfile.TemporaryDirectory() as report_dir:
        try:
            stdout = run(tool.command(scope, report_dir), root, cap_seconds)
        except subprocess.TimeoutExpired:
            return {**unavailable(f"{tool.name} hit the {cap_seconds:g}s cap"), "tool_version": version}
        counts = tool.counts(stdout, report_dir)
    detected = counts.killed + counts.timed_out
    total = detected + counts.survived
    result.update(killed=counts.killed, survived=counts.survived, timed_out=counts.timed_out)
    result["mutation_score"] = detected / total if total else Unavailable("the tool found no mutants in the scope")
    result["sample_size"] = total
    result["duration_ms"] = elapsed_ms()
    return result
