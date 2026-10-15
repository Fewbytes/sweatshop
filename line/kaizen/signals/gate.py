"""Pre-submit gate: measure the working tree against a base revision and fail
on new findings in the files the change touched (kaizen.md, section 3).

Both trees go through measure.measure with the history of the base revision, so
the two runs differ only by the edit. Cycles and clones span files, so the
whole tree is measured; only findings in touched files and their packages are
reported.
"""

import json
import os
import subprocess
import tempfile
from dataclasses import dataclass

import measure
from render import number
from syntax import package_of

THRESHOLDS_FILE = "kaizen-gate.json"


@dataclass(frozen=True)
class Thresholds:
    """How much a signal may grow in a touched file or package before the gate fails."""

    max_complexity_increase: int = 0
    max_new_import_cycles: int = 0
    max_new_clones: int = 0
    max_hotspot_rank_rise: int | None = None  # None: the rank change is reported, never fails


@dataclass(frozen=True)
class Finding:
    key: str  # stable id a baseline file lists to accept it, e.g. "complexity:a/a.go"
    message: str


def load_thresholds(path: str | None) -> Thresholds:
    """Thresholds from a JSON object keyed by Thresholds field names. No path
    means the defaults; an unknown key is an error, so a typo cannot disable a check."""
    if path is None:
        return Thresholds()
    with open(path, encoding="utf-8") as f:
        return Thresholds(**json.load(f))


def load_baseline(path: str | None) -> set[str]:
    """Finding keys of existing debt: a JSON list of Finding.key."""
    if path is None:
        return set()
    with open(path, encoding="utf-8") as f:
        return set(json.load(f))


def changed_files(root: str, base: str) -> list[str]:
    """Repo-relative paths modified, added or untracked against base, sorted."""
    tracked = measure.git(root, "diff", "--name-only", "--no-renames", base).splitlines()
    untracked = measure.git(root, "ls-files", "--others", "--exclude-standard").splitlines()
    return sorted(set(tracked) | set(untracked))


def measure_base(root: str, base: str) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        tree = os.path.join(tmp, "base")
        measure.git(root, "worktree", "add", "--detach", tree, base)
        try:
            return measure.measure(tree, base, None)
        finally:
            measure.git(root, "worktree", "remove", "--force", tree)


def compare(before: dict, after: dict, touched: list[str], limits: Thresholds) -> tuple[list[str], list[Finding]]:
    """(report lines, findings over a threshold) for the touched files."""
    report: list[str] = []
    findings: list[Finding] = []
    for path in touched:
        new = after["files"].get(path)
        if new is None:
            continue
        old = before["files"].get(path, {"complexity": 0, "hotspot_rank": None})
        grown = new["complexity"] - old["complexity"]
        report.append(f"{path}: complexity {old['complexity']} → {new['complexity']}, hotspot rank {old['hotspot_rank']} → {new['hotspot_rank']}")
        if grown > limits.max_complexity_increase:
            findings.append(Finding(f"complexity:{path}", f"{path}: complexity grew by {grown} (allowed {limits.max_complexity_increase})"))
        if old["hotspot_rank"] is not None and limits.max_hotspot_rank_rise is not None:
            rise = old["hotspot_rank"] - new["hotspot_rank"]
            if rise > limits.max_hotspot_rank_rise:
                findings.append(Finding(f"hotspot:{path}", f"{path}: hotspot rank rose by {rise} places (allowed {limits.max_hotspot_rank_rise})"))
    for pkg in sorted({package_of(p) for p in touched} & after["packages"].keys()):
        old = before["packages"].get(pkg, {"import_cycles": 0, "clones": 0})
        new = after["packages"][pkg]
        for signal, key, allowed in (
            ("import_cycles", "cycle", limits.max_new_import_cycles),
            ("clones", "clones", limits.max_new_clones),
        ):
            grown = number(new[signal]) - number(old[signal])
            if grown:
                report.append(f"package {pkg}: {signal} {old[signal]} → {new[signal]}")
            if grown > allowed:
                findings.append(Finding(f"{key}:{pkg}", f"package {pkg}: {signal} grew by {grown} (allowed {allowed})"))
    return report, findings


def run_gate(root: str, base: str, limits: Thresholds, accepted: set[str]) -> tuple[list[str], list[Finding]]:
    """Report lines and the findings that are neither under threshold nor in the baseline."""
    touched = changed_files(root, base)
    before = measure_base(root, base)
    after = measure.measure(root, base, None)
    if before["tools"] != after["tools"]:
        raise RuntimeError("tool versions differ between the two measurements")
    report, findings = compare(before, after, touched, limits)
    return report, [f for f in findings if f.key not in accepted]
