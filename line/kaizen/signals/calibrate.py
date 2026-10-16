"""Do the file signals predict where bugs land on this repo? (kaizen.md, section 3)

Prior art: codelore's `calibrate-defects` (research-docs/code-health-signals/README.md).
A closed bug card's fix is the first-parent commits carrying its `Bead:` trailer.
At the revision before the first of them the signals of every source file are
measured; the existing files the fix touched are the positives and all other
source files the negatives. Each signal is scored per fix and averaged over the
fixes, so no fix is judged against files of another revision:

  auc          chance that a touched file outranks an untouched one (0.5 is chance;
               ties count half)
  precision@k  share of the signal's top k files that were touched, k being the
               number of touched files (R-precision); `chance` is the same share
               for a random ranking

`loc` is a baseline: size confounds most metrics (El Emam et al. 2001), so a
signal earns its place only by beating it.
"""

import math
import os
import subprocess
import tempfile
from collections import defaultdict
from collections.abc import Callable
from dataclasses import dataclass

import measure
import rework

# Signals of signals.json `files`; larger is riskier for each.
SIGNALS = ("hotspot", "complexity", "hidden_pairs", "churn", "cards", "rework_rate", "loc")
# Fewer fixes than this cannot separate a signal from chance; the line is young.
MIN_FIXES = 10
# A fix touching more files than this is bulk (file_signals.MAX_FILES_PER_COMMIT), not a locus of the bug.
MAX_FILES_PER_FIX = 50
Z_95 = 1.96


@dataclass(frozen=True)
class FixCase:
    card: str
    base: str  # revision before the fix
    touched: frozenset[str]  # paths the fix commits changed


def fix_cases(root: str, head: str, cards: list[dict]) -> tuple[list[FixCase], dict[str, str]]:
    """Fix cases of the closed bug cards that landed in head's first-parent history, and why others are skipped."""
    bugs = {c["id"] for c in cards if c.get("issue_type") == "bug"}
    history = rework.read_history(root, head)
    commits_of: dict[str, list[rework.HistoryCommit]] = defaultdict(list)
    for commit in history:
        if (card := rework.card_of(commit)) in bugs:
            commits_of[card].append(commit)
    cases, skipped = [], {}
    for card in sorted(bugs):
        commits = commits_of.get(card)
        if not commits:
            skipped[card] = "no commit carries its Bead trailer"
        elif not commits[0].parents:
            skipped[card] = "fix is the root commit"
        else:
            touched = frozenset(path for c in commits for path in c.added)
            if len(touched) > MAX_FILES_PER_FIX:
                skipped[card] = f"fix touches {len(touched)} files, a bulk change"
            else:
                cases.append(FixCase(card, commits[0].parents[0], touched))
    return cases, skipped


def auc(scores: dict[str, float], positives: frozenset[str]) -> float | None:
    """Mann-Whitney AUC of scores for positives against the other paths; None without both kinds."""
    pos = [s for p, s in scores.items() if p in positives]
    neg = [s for p, s in scores.items() if p not in positives]
    if not pos or not neg:
        return None
    wins = sum((p > n) + 0.5 * (p == n) for p in pos for n in neg)
    return wins / (len(pos) * len(neg))


def precision_at_positives(scores: dict[str, float], positives: frozenset[str]) -> float:
    """Share of the top len(positives) paths that are positives; ties broken by path so the result is deterministic."""
    k = len(positives & scores.keys())
    top = sorted(scores, key=lambda p: (-scores[p], p))[:k]
    return sum(p in positives for p in top) / k


def mean_and_error(values: list[float]) -> tuple[float, float]:
    mean = sum(values) / len(values)
    if len(values) < 2:
        return mean, math.inf
    variance = sum((v - mean) ** 2 for v in values) / (len(values) - 1)
    return mean, math.sqrt(variance / len(values))


def verdict(aucs: list[float]) -> str:
    """predictive / noise / inconclusive from the 95% interval of the mean per-fix AUC around 0.5."""
    if len(aucs) < MIN_FIXES:
        return "inconclusive"
    mean, error = mean_and_error(aucs)
    if mean - Z_95 * error > 0.5:
        return "predictive"
    if mean + Z_95 * error < 0.5:
        return "anti-predictive"
    return "noise"


def calibrate(cases: list[FixCase], measure_at: Callable[[str], dict[str, dict]]) -> dict:
    """Per-signal AUC and precision@k over the fix cases; measure_at(base) returns signals.json `files`."""
    files_at: dict[str, dict[str, dict]] = {}
    per_signal: dict[str, dict[str, list[float]]] = {s: {"auc": [], "precision": [], "chance": []} for s in SIGNALS}
    used = 0
    for case in cases:
        files = files_at.setdefault(case.base, measure_at(case.base))
        positives = case.touched & files.keys()
        if not positives or positives == files.keys():
            continue
        used += 1
        for signal in SIGNALS:
            scores = {path: float(f[signal]) for path, f in files.items()}
            per_signal[signal]["auc"].append(auc(scores, positives))
            per_signal[signal]["precision"].append(precision_at_positives(scores, positives))
            per_signal[signal]["chance"].append(len(positives) / len(files))
    signals = {}
    for signal, got in per_signal.items():
        aucs = got["auc"]
        if not aucs:
            signals[signal] = {"fixes": 0, "verdict": "inconclusive"}
            continue
        mean, error = mean_and_error(aucs)
        signals[signal] = {
            "fixes": len(aucs), "auc": mean, "auc_error": None if math.isinf(error) else error,
            "precision_at_k": sum(got["precision"]) / len(aucs), "chance": sum(got["chance"]) / len(aucs),
            "verdict": verdict(aucs),
        }
    return {"fixes_used": used, "fixes_found": len(cases), "min_fixes": MIN_FIXES, "signals": signals}


def measure_files_in_worktree(root: str, rev: str) -> dict[str, dict]:
    """signals.json `files` of rev, measured on a temporary checkout so the working tree is untouched."""
    with tempfile.TemporaryDirectory() as tmp:
        checkout = os.path.join(tmp, "checkout")
        rework.run_git(root, "worktree", "add", "--detach", checkout, rev)
        try:
            return measure.measure(checkout, rev, None)["files"]
        finally:
            subprocess.run(["git", "-C", root, "worktree", "remove", "--force", checkout], check=True, capture_output=True)


def run_calibration(root: str, head: str, cards: list[dict]) -> dict:
    cases, skipped = fix_cases(root, head, cards)
    result = calibrate(cases, lambda base: measure_files_in_worktree(root, base))
    return {**result, "skipped": skipped}


def render_markdown(result: dict) -> str:
    lines = [
        "# Do the signals predict bugs on this repo?", "",
        f"{result['fixes_used']} of {result['fixes_found']} bug-card fixes could be scored"
        f" ({len(result['skipped'])} bug cards have no usable fix). A verdict needs at least {result['min_fixes']}"
        " scored fixes; the line is young, so re-run as history grows.", "",
        "| signal | fixes | AUC (±95%) | precision@k | chance | verdict |", "|---|---|---|---|---|---|",
    ]
    for name, s in result["signals"].items():
        if not s["fixes"]:
            lines.append(f"| {name} | 0 | n/a | n/a | n/a | {s['verdict']} |")
            continue
        error = "n/a" if s["auc_error"] is None else f"{Z_95 * s['auc_error']:.2f}"
        lines.append(f"| {name} | {s['fixes']} | {s['auc']:.2f} ± {error} | {s['precision_at_k']:.0%} | {s['chance']:.0%} | {s['verdict']} |")
    return "\n".join(lines) + "\n"
