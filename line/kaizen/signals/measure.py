"""Compute the code-health signals of kaizen.md section 3 by running the
language adapters and recording the tools behind the numbers.

Deterministic: the clock is the history revision's commit date, never the wall
clock, and every collection is sorted before it is written.
"""

import json
import platform
import statistics
import subprocess
from datetime import datetime, timedelta

import clone_signals
from adapter import Adapter, AdapterResult, Commit, History, Signals, Unavailable
from lang_go import GO
from lang_python import PYTHON
from lang_typescript import TYPESCRIPT
from source_adapter import SourceAdapter

SCHEMA = 7
WINDOW_DAYS = 90
CI_RUNS_CONSIDERED = 50
SECONDS_PER_YEAR = 365.25 * 24 * 3600
# Every language adapter, run in this order. A literal table so search finds
# each one; adding a language is adding a line here.
ADAPTERS: tuple[Adapter, ...] = (SourceAdapter(GO), SourceAdapter(PYTHON), SourceAdapter(TYPESCRIPT))


def git(root: str, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", root, *args], check=True, capture_output=True, text=True
    ).stdout


def history(root: str, rev: str) -> History:
    head_date = git(root, "log", "-1", "--format=%cI", rev).strip()
    head = datetime.fromisoformat(head_date)
    since = (head - timedelta(days=WINDOW_DAYS)).isoformat()
    out = git(
        root, "log", rev, f"--since={since}", "--numstat", "--no-renames",
        "--format=@@%H %cI @%(trailers:key=Bead,valueonly,separator=%x2C)",
    )
    commits: list[Commit] = []
    for line in out.splitlines():
        if line.startswith("@@"):
            sha, rest = line[2:].split(" ", 1)
            date, card = rest.split(" @", 1)
            age_years = (head - datetime.fromisoformat(date)).total_seconds() / SECONDS_PER_YEAR
            commits.append(Commit(card.strip() or None, {}, {}, age_years, sha))
        elif line:
            added, deleted, path = line.split("\t", 2)
            # "-" is a binary file
            commits[-1].deleted_lines[path] = 0 if deleted == "-" else int(deleted)
            commits[-1].changed_lines[path] = 0 if added == "-" else int(added) + int(deleted)
    return History(head_date, commits)


def ci_runtime(runs: dict | None) -> Signals:
    """Test runtime from the CI metrics store: GET /v1/runs saved as JSON."""
    if runs is None:
        return {"ci_runs": 0, "ci_median_ms": Unavailable("no CI runs file given")}
    durations = [r["duration_ms"] for r in runs["runs"] if r["passed"]][:CI_RUNS_CONSIDERED]
    return {
        "ci_runs": len(durations),
        "ci_median_ms": int(statistics.median(durations)) if durations else Unavailable("no passing CI run"),
    }


def tool_versions(adapters: tuple[Adapter, ...]) -> dict[str, str]:
    """Versions of everything the numbers depend on. A difference between two
    runs makes them different baselines (see render.baseline_mismatch)."""
    tools = {
        "git": git(".", "--version").split()[-1],
        "python": platform.python_version(),
    }
    for adapter in adapters:
        tools.update(adapter.tool_versions())
    return dict(sorted(tools.items()))


def check_declared(adapter: Adapter, result: AdapterResult) -> None:
    for pkg, signals in result.packages.items():
        missing = set(adapter.package_signals) - signals.keys()
        if missing:
            raise ValueError(f"{adapter.language} adapter omitted {sorted(missing)} for package {pkg}")
    missing = set(adapter.repo_signals) - result.repo.keys()
    if missing:
        raise ValueError(f"{adapter.language} adapter omitted repo signals {sorted(missing)}")


def measure(root: str, rev: str, runs: dict | None, adapters: tuple[Adapter, ...] = ADAPTERS) -> dict:
    hist = history(root, rev)
    packages: dict[str, Signals] = {}
    repo: Signals = {}
    files: dict[str, Signals] = {}
    cochange_pairs: list[Signals] = []
    hotspot_functions: list[Signals] = []
    clone_pairs: list[Signals] = []
    module_edges: list[Signals] = []
    modularity_violations: dict[str, list[Signals] | Unavailable] = {}
    for adapter in adapters:
        result = adapter.measure(root, hist)
        check_declared(adapter, result)
        shared = packages.keys() & result.packages.keys()
        if shared:
            raise ValueError(f"{adapter.language} adapter and an earlier one both claim packages {sorted(shared)}")
        packages.update(result.packages)
        repo.update(result.repo)
        files.update(result.files)
        cochange_pairs += result.cochange_pairs
        hotspot_functions += result.hotspot_functions
        clone_pairs += result.clone_pairs
        module_edges += result.module_edges
        modularity_violations[adapter.language] = result.modularity_violations
    repo.update(ci_runtime(runs))
    return {
        "schema": SCHEMA,
        "commit": git(root, "rev-parse", rev).strip(),
        "measured_at": hist.measured_at,
        "window_days": WINDOW_DAYS,
        "tools": tool_versions(adapters),
        "repo": repo,
        "packages": packages,
        "files": files,
        "cochange_pairs": sorted(cochange_pairs, key=lambda p: (-p["count"], p["a"], p["b"])),
        "clone_pairs": sorted(clone_pairs, key=clone_signals.rank_key),
        "module_edges": sorted(module_edges, key=lambda e: (e["a"], e["b"])),
        "modularity_violations": modularity_violations,
        "hotspot_functions": sorted(hotspot_functions, key=lambda f: (-f["hotspot"], f["file"], f["name"])),
    }


def _encode(value: object) -> dict:
    if isinstance(value, Unavailable):
        return {"unavailable": value.reason}
    raise TypeError(f"cannot serialize {type(value).__name__}")


def dumps(signals: dict) -> str:
    return json.dumps(signals, indent=2, sort_keys=True, default=_encode) + "\n"
