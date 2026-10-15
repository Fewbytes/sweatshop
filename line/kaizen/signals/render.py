"""Render signals.json as the per-package status report (report.html) and
compare two measurements."""

import html
from dataclasses import dataclass

# Package-level signals, all "lower is better", with their column titles.
PACKAGE_SIGNALS = {
    "hotspot": "Hotspot (churn × complexity)",
    "churn": "Churn",
    "complexity": "Cognitive complexity",
    "hidden_coupling_ratio": "Hidden coupling",
    "clones": "Clones",
    "import_cycles": "Import cycles",
    "dead_symbols": "Dead symbols",
    "pattern_entropy": "Pattern entropy",
}
# Repo-level signals: the package ones summed, plus CI runtime.
REPO_SIGNALS = {
    "hotspot": "Hotspot total",
    "complexity": "Cognitive complexity",
    "hidden_coupling_ratio": "Hidden coupling",
    "clones": "Clones",
    "import_cycles": "Import cycles",
    "dead_symbols": "Dead symbols",
    "pattern_entropy": "Pattern entropy",
    "ci_median_ms": "CI median runtime (ms)",
}
STYLE = (
    "body{font:14px system-ui,sans-serif;margin:2rem}table{border-collapse:collapse}"
    "td,th{border:1px solid #bbb;padding:2px 8px;text-align:right}td:first-child,th:first-child{text-align:left}"
    ".better{color:#0a6b2d}.worse{color:#a4161a}"
)


@dataclass
class Pick:
    target: str
    category: str
    signal: str
    why: str


def fmt_delta(old, new) -> str:
    """The value, then its change against old as a coloured span; no span when
    old is unknown or equal."""
    value = "–" if new is None else f"{new:g}"
    if old is None or new is None or old == new:
        return value
    diff = new - old
    css = "better" if diff < 0 else "worse"
    return f'{value} <span class="{css}">({diff:+g})</span>'


def _table(headers: list[str], rows: list[list[str]]) -> str:
    head = "".join(f"<th>{html.escape(h)}</th>" for h in headers)
    body = "".join("<tr>" + "".join(f"<td>{c}</td>" for c in row) + "</tr>" for row in rows)
    return f"<table><tr>{head}</tr>{body}</table>"


def status_table(current: dict, previous: dict | None) -> str:
    prev_pkgs = (previous or {}).get("packages", {})
    rows = []
    for pkg, cur in sorted(current["packages"].items(), key=lambda kv: kv[1]["hotspot_rank"]):
        old = prev_pkgs.get(pkg)
        row = [html.escape(pkg), str(cur["hotspot_rank"])]
        row += [fmt_delta(old[s] if old else None, cur[s]) for s in PACKAGE_SIGNALS]
        rows.append(row)
    return _table(["Package", "Rank", *PACKAGE_SIGNALS.values()], rows)


def repo_table(current: dict, previous: dict | None, after: dict | None) -> str:
    rows = []
    for key, title in REPO_SIGNALS.items():
        before = current["repo"][key]
        row = [html.escape(title), fmt_delta((previous or {})["repo"].get(key) if previous else None, before)]
        if after is not None:
            row.append(fmt_delta(before, after["repo"][key]))
        rows.append(row)
    return _table(["Signal", "Baseline (trend vs previous run)", *(["After the change"] if after else [])], rows)


def package_change_table(before: dict, after: dict) -> str:
    rows = []
    for pkg in sorted(set(before["packages"]) | set(after["packages"])):
        b, a = before["packages"].get(pkg), after["packages"].get(pkg)
        changed = [(s, b[s] if b else None, a[s] if a else None) for s in PACKAGE_SIGNALS]
        if any(old != new for _, old, new in changed):
            rows.append([html.escape(pkg), *[fmt_delta(old, new) for _, old, new in changed]])
    return _table(["Package", *PACKAGE_SIGNALS.values()], rows) if rows else "<p>No package signal changed.</p>"


def render_report(current: dict, previous: dict | None, after: dict | None, pick: Pick | None) -> str:
    parts = [
        f"<!doctype html><meta charset=utf-8><title>Kaizen status {html.escape(current['commit'][:12])}</title>",
        f"<style>{STYLE}</style>",
        f"<h1>Kaizen status at {html.escape(current['commit'][:12])}</h1>",
        f"<p>History window {current['window_days']} days ending {html.escape(current['measured_at'])}; "
        + (f"trend against {html.escape(previous['commit'][:12])}." if previous else "no previous run to compare."),
        "</p><h2>Repository</h2>",
        repo_table(current, previous, after),
        "<h2>Modules by hotspot rank</h2>",
        status_table(current, previous),
    ]
    if pick:
        parts += [
            "<h2>Chosen target</h2>",
            f"<p><b>{html.escape(pick.target)}</b> — {html.escape(pick.category)}; "
            f"moves <b>{html.escape(pick.signal)}</b>.</p><p>{html.escape(pick.why)}</p>",
        ]
    if after is not None:
        parts += ["<h2>Before / after: packages whose signals changed</h2>", package_change_table(current, after)]
    return "\n".join(parts) + "\n"


def signal_value(signals: dict, package: str, signal: str):
    """Package is a package directory, or '.' for the repo-wide value."""
    return (signals["repo"] if package == "." else signals["packages"][package])[signal]


def regressions(before: dict, after: dict, package: str, except_signal: str) -> list[str]:
    """Other signals of the package that got worse, as 'signal: old → new'."""
    worse = []
    for s in REPO_SIGNALS if package == "." else PACKAGE_SIGNALS:
        old, new = signal_value(before, package, s), signal_value(after, package, s)
        if s != except_signal and old is not None and new is not None and new > old:
            worse.append(f"{s}: {old:g} → {new:g}")
    return worse
