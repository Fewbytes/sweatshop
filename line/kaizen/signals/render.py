"""Render signals.json as the per-package status report (report.html) and
compare two measurements."""

import html
from dataclasses import dataclass

import graphs

# Package-level signals, all "lower is better", with their column titles.
PACKAGE_SIGNALS = {
    "hotspot": "Hotspot (decayed churn × complexity)",
    "hotspot_per_loc": "Hotspot per LOC",
    "churn": "Churn",
    "relative_churn": "Relative churn (changed LOC / LOC)",
    "complexity": "Cognitive complexity",
    "lcom4": "Cohesion (LCOM4 components)",
    "clones": "Clones",
    "import_cycles": "Import cycles",
    "dead_symbols": "Dead symbols",
    "pattern_entropy": "Pattern entropy",
}
# Repo-level signals: the package ones summed, plus CI runtime.
REPO_SIGNALS = {
    "hotspot": "Hotspot total",
    "complexity": "Cognitive complexity",
    "clones": "Clones",
    "import_cycles": "Import cycles",
    "dead_symbols": "Dead symbols",
    "pattern_entropy": "Pattern entropy",
    "ci_median_ms": "CI median runtime (ms)",
}
# The graph views draw the modules ranked highest by hotspot, so the picture stays readable.
GRAPH_MODULES = 25
COHESION_GRAPH_MODULES = 8
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


def number(value):
    """A signal's number, or None when the adapter reported it unavailable."""
    return None if isinstance(value, dict) and "unavailable" in value else value


def baseline_mismatch(before: dict, after: dict) -> str | None:
    """Why two runs are not comparable, or None. A run measured with other tool
    versions (or an older schema, which recorded none) starts a new baseline."""
    if before.get("tools") == after.get("tools"):
        return None
    return f"tool versions differ ({before.get('tools')} vs {after.get('tools')}): new baseline"


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
        row += [fmt_delta(number(old[s]) if old else None, number(cur[s])) for s in PACKAGE_SIGNALS]
        rows.append(row)
    return _table(["Package", "Rank", *PACKAGE_SIGNALS.values()], rows)


def repo_table(current: dict, previous: dict | None, after: dict | None) -> str:
    rows = []
    for key, title in REPO_SIGNALS.items():
        before = number(current["repo"][key])
        row = [html.escape(title), fmt_delta(number(previous["repo"].get(key)) if previous else None, before)]
        if after is not None:
            row.append(fmt_delta(before, number(after["repo"][key])))
        rows.append(row)
    return _table(["Signal", "Baseline (trend vs previous run)", *(["After the change"] if after else [])], rows)


def package_change_table(before: dict, after: dict) -> str:
    rows = []
    for pkg in sorted(set(before["packages"]) | set(after["packages"])):
        b, a = before["packages"].get(pkg), after["packages"].get(pkg)
        changed = [(s, number(b[s]) if b else None, number(a[s]) if a else None) for s in PACKAGE_SIGNALS]
        if any(old != new for _, old, new in changed):
            rows.append([html.escape(pkg), *[fmt_delta(old, new) for _, old, new in changed]])
    return _table(["Package", *PACKAGE_SIGNALS.values()], rows) if rows else "<p>No package signal changed.</p>"


def dependency_section(current: dict) -> str:
    ranked = sorted(current["packages"], key=lambda p: current["packages"][p]["hotspot_rank"])[:GRAPH_MODULES]
    hidden = sorted((e for e in current["module_edges"] if e["hidden"]), key=lambda e: (-e["cochange"], e["a"], e["b"]))
    rows = [[html.escape(e["a"]), html.escape(e["b"]), str(e["cochange"])] for e in hidden]
    return "\n".join([
        "<h2>Module dependency graph</h2>",
        f"<p>The {len(ranked)} modules of highest hotspot rank. Solid grey: static imports, width by import count. "
        "Dashed red: hidden coupling, modules that change together with no import between them, width by shared commits.</p>",
        graphs.dependency_graph(current["module_edges"], sorted(ranked)),
        "<h3>Hidden coupling: co-change without an import</h3>",
        _table(["Module", "Module", "Shared commits"], rows) if rows else "<p>None.</p>",
    ])


def violations_section(current: dict) -> str:
    parts = [
        "<h2>Modularity violations</h2>",
        "<p>File pairs that changed together with no import either way, most supported first: "
        "support is shared commits, confidence the share of the busier file's commits.</p>",
    ]
    for language, found in sorted(current["modularity_violations"].items()):
        parts.append(f"<h3>{html.escape(language)}</h3>")
        if "unavailable" in found:
            parts.append(f"<p>Not measured: {html.escape(found['unavailable'])}.</p>")
        elif found:
            rows = [
                [html.escape(v["a"]), html.escape(v["b"]), str(v["support"]), f"{v['confidence']:.0%}", html.escape(v["last_card"] or "–")]
                for v in found
            ]
            parts.append(_table(["File", "File", "Support", "Confidence", "Last card"], rows))
        else:
            parts.append("<p>None.</p>")
    return "\n".join(parts)


def cohesion_section(current: dict) -> str:
    chosen = [
        p for p, s in sorted(current["packages"].items(), key=lambda kv: kv[1]["hotspot_rank"])
        if s["symbol_edges"] and (s["lcom4"] > 1 or s["hotspot_rank"] <= COHESION_GRAPH_MODULES)
    ][:COHESION_GRAPH_MODULES]
    parts = ["<h2>Module cohesion graphs</h2>", "<p>Nodes are a module's top-level symbols, edges are references between them; "
             "one colour per connected component. Shown for the top-ranked modules and any module whose files form several components.</p>"]
    for pkg in chosen:
        svg, drawn = graphs.cohesion_graph(current["packages"][pkg]["symbol_edges"])
        parts.append(f"<h3>{html.escape(pkg)} (LCOM4 {current['packages'][pkg]['lcom4']}, {drawn} connected symbols drawn)</h3>{svg}")
    return "\n".join(parts)


def render_report(current: dict, previous: dict | None, after: dict | None, pick: Pick | None) -> str:
    new_baseline = baseline_mismatch(previous, current) if previous else None
    if new_baseline:
        previous = None
    if after is not None and baseline_mismatch(current, after):
        raise ValueError(f"cannot compare the after run: {baseline_mismatch(current, after)}")
    parts = [
        f"<!doctype html><meta charset=utf-8><title>Kaizen status {html.escape(current['commit'][:12])}</title>",
        f"<style>{STYLE}</style>",
        f"<h1>Kaizen status at {html.escape(current['commit'][:12])}</h1>",
        f"<p>History window {current['window_days']} days ending {html.escape(current['measured_at'])}; "
        + (f"trend against {html.escape(previous['commit'][:12])}." if previous else "no previous run to compare."),
        "</p>",
        f"<p>{html.escape(new_baseline)}.</p>" if new_baseline else "",
        "<p>Tools: " + html.escape(", ".join(f"{k} {v}" for k, v in current["tools"].items())) + ".</p>",
        "<h2>Repository</h2>",
        repo_table(current, previous, after),
        "<h2>Modules by hotspot rank</h2>",
        status_table(current, previous),
        dependency_section(current),
        violations_section(current),
        cohesion_section(current),
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
    return number((signals["repo"] if package == "." else signals["packages"][package])[signal])


def regressions(before: dict, after: dict, package: str, except_signal: str) -> list[str]:
    """Other signals of the package that got worse, as 'signal: old → new'."""
    worse = []
    for s in REPO_SIGNALS if package == "." else PACKAGE_SIGNALS:
        old, new = signal_value(before, package, s), signal_value(after, package, s)
        if s != except_signal and old is not None and new is not None and new > old:
            worse.append(f"{s}: {old:g} → {new:g}")
    return worse
