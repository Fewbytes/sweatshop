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

from adapter import Adapter, AdapterResult, History, Signals, Unavailable
from lang_go import GO
from lang_python import PYTHON
from lang_typescript import TYPESCRIPT
from source_adapter import SourceAdapter

SCHEMA = 2
WINDOW_DAYS = 90
CI_RUNS_CONSIDERED = 50
# Every language adapter, run in this order. A literal table so search finds
# each one; adding a language is adding a line here.
ADAPTERS: tuple[Adapter, ...] = (SourceAdapter(GO), SourceAdapter(PYTHON), SourceAdapter(TYPESCRIPT))


def git(root: str, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", root, *args], check=True, capture_output=True, text=True
    ).stdout


def history(root: str, rev: str) -> History:
    head_date = git(root, "log", "-1", "--format=%cI", rev).strip()
    since = (datetime.fromisoformat(head_date) - timedelta(days=WINDOW_DAYS)).isoformat()
    out = git(root, "log", rev, f"--since={since}", "--name-only", "--format=@@%H")
    commits: list[list[str]] = []
    for line in out.splitlines():
        if line.startswith("@@"):
            commits.append([])
        elif line:
            commits[-1].append(line)
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
    for adapter in adapters:
        result = adapter.measure(root, hist)
        check_declared(adapter, result)
        shared = packages.keys() & result.packages.keys()
        if shared:
            raise ValueError(f"{adapter.language} adapter and an earlier one both claim packages {sorted(shared)}")
        packages.update(result.packages)
        repo.update(result.repo)
    repo.update(ci_runtime(runs))
    return {
        "schema": SCHEMA,
        "commit": git(root, "rev-parse", rev).strip(),
        "measured_at": hist.measured_at,
        "window_days": WINDOW_DAYS,
        "tools": tool_versions(adapters),
        "repo": repo,
        "packages": packages,
    }


def _encode(value: object) -> dict:
    if isinstance(value, Unavailable):
        return {"unavailable": value.reason}
    raise TypeError(f"cannot serialize {type(value).__name__}")


def dumps(signals: dict) -> str:
    return json.dumps(signals, indent=2, sort_keys=True, default=_encode) + "\n"
