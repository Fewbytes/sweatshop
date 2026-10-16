"""File-level signals from git history and the tree-sitter facts: hotspots,
co-change, hidden coupling, cards per hotspot, rework rate and cohesion.

All of it is language-agnostic: it reads SourceFile facts and the package
import graph, never a grammar. Definitions are in kaizen.md section 3.
"""

import math
from collections import Counter, defaultdict
from dataclasses import dataclass
from itertools import combinations

from adapter import Commit, Signals
from syntax import SourceFile, package_of

# A transaction touching more packages or files than this is a bulk change
# (rename, format), not evidence that the files depend on each other. 30 files
# is code-maat's default.
MAX_PACKAGES_PER_TRANSACTION = 20
MAX_FILES_PER_TRANSACTION = 30
MIN_COCHANGES = 2
# Fewer transactions than this back a number too thinly to trust: the line's
# history is short and small samples make instability look like signal
# (Kirbas et al. 2017). Our choice, not a validated cut-off.
MIN_TRANSACTIONS_FOR_CONFIDENCE = 5
TOP_COCHANGE_PAIRS = 50
TOP_HOTSPOT_FUNCTIONS = 20
# Graves et al. 2000 weight a change by exp(-alpha * age), alpha 0.75 per year.
DECAY_PER_YEAR = 0.75


@dataclass(frozen=True)
class Transaction:
    card: str | None  # None for a commit that names no card
    files: frozenset[str]


def transactions(commits: list[Commit]) -> list[Transaction]:
    """The change transactions in the commits, newest first.

    Zimmermann et al. count support over transactions. An agent commits one
    card in several small pieces, so the commits of one card (Bead trailer) are
    one transaction; a commit naming no card is its own. Transactions over
    MAX_FILES_PER_TRANSACTION files are dropped as bulk changes."""
    touched: dict[str, set[str]] = {}
    for commit in commits:  # newest first, as git log gives them
        touched.setdefault(commit.card or commit.sha, set()).update(commit.deleted_lines)
    cards = {c.card or c.sha: c.card for c in commits}
    return [Transaction(cards[key], frozenset(files)) for key, files in touched.items() if len(files) <= MAX_FILES_PER_TRANSACTION]


@dataclass(frozen=True)
class ViolationThresholds:
    """When a co-changing pair with no import is reported as a modularity
    violation. The defaults are code-maat's (min shared commits 5, min
    confidence 30%); they are our choice, not a validated cut-off."""

    min_support: int = 5
    min_confidence: float = 0.3


def decayed_changes(commits: list[Commit], source: dict[str, SourceFile]) -> Counter[str]:
    """Per source file, the sum over its commits of exp(-DECAY_PER_YEAR * age) *
    log(1 + lines changed): Graves et al.'s decayed change count, with the log
    of the change size. The 1 keeps a one-line change from weighing nothing."""
    score: Counter[str] = Counter()
    for commit in commits:
        weight = math.exp(-DECAY_PER_YEAR * commit.age_years)
        for path, lines in commit.changed_lines.items():
            if path in source:
                score[path] += weight * math.log1p(lines)
    return score


def structurally_unlinked(a: str, b: str, package_graph: dict[str, set[str]]) -> bool:
    """True when the files are in different directories and neither directory imports the other."""
    pa, pb = package_of(a), package_of(b)
    return pa != pb and pb not in package_graph[pa] and pa not in package_graph[pb]


def modularity_violations(
    source: dict[str, SourceFile],
    commits: list[Commit],
    file_pairs: Counter[tuple[str, str]],
    package_graph: dict[str, set[str]],
    thresholds: ViolationThresholds,
) -> list[Signals]:
    """File pairs that co-change with no import either way (Mo et al.'s
    modularity violations), most supported first. support is the shared
    transactions; confidence is Zimmermann's, support over the transactions of
    the pair's busier file, the weaker of the two rules a=>b and b=>a.
    low_confidence is true when fewer than MIN_TRANSACTIONS_FOR_CONFIDENCE
    transactions back the pair. last_card is the newest shared transaction that
    names a card, None if none does."""
    history = transactions(commits)
    churn = Counter(path for t in history for path in t.files if path in source)
    found = {}
    for (a, b), support in file_pairs.items():
        if a not in source or b not in source:  # test files co-change with what they test
            continue
        confidence = support / max(churn[a], churn[b])
        if (
            support >= thresholds.min_support
            and confidence >= thresholds.min_confidence
            and structurally_unlinked(a, b, package_graph)
        ):
            found[a, b] = (support, confidence)
    last_card: dict[tuple[str, str], str] = {}
    for t in history:  # newest first
        if t.card:
            for pair in combinations(sorted(p for p in t.files if p in source), 2):
                if pair in found:
                    last_card.setdefault(pair, t.card)
    ranked = sorted(found, key=lambda pair: (-found[pair][0], -found[pair][1], pair))
    return [
        {"a": a, "b": b, "support": found[a, b][0], "confidence": round(found[a, b][1], 3),
         "low_confidence": found[a, b][0] < MIN_TRANSACTIONS_FOR_CONFIDENCE, "last_card": last_card.get((a, b))}
        for a, b in ranked
    ]


def cochanging_pairs(
    commits: list[Commit], files: dict[str, SourceFile]
) -> tuple[Counter[tuple[str, str]], Counter[tuple[str, str]]]:
    """(package pairs, file pairs) -> number of non-bulk transactions touching both."""
    package_pairs: Counter[tuple[str, str]] = Counter()
    file_pairs: Counter[tuple[str, str]] = Counter()
    for t in transactions(commits):
        touched = sorted(p for p in t.files if p in files)
        pkgs = sorted({package_of(p) for p in touched})
        if len(pkgs) > MAX_PACKAGES_PER_TRANSACTION:
            continue
        package_pairs.update(combinations(pkgs, 2))
        file_pairs.update(combinations(touched, 2))
    return package_pairs, file_pairs


def lcom4(sources: dict[str, SourceFile]) -> int:
    """Connected components of the files of one package, two files being linked
    when one uses a name the other declares. 1 is fully cohesive; each extra
    component is a group of files that never reference the rest."""
    parent = {p: p for p in sources}

    def find(p: str) -> str:
        while parent[p] != p:
            parent[p] = parent[parent[p]]
            p = parent[p]
        return p

    declared_in: dict[str, list[str]] = defaultdict(list)
    for path, f in sources.items():
        for name in f.symbols:
            declared_in[name].append(path)
    for path, f in sources.items():
        for name in set(f.identifiers):
            for other in declared_in.get(name, ()):
                parent[find(path)] = find(other)
    return len({find(p) for p in sources}) or 1


def symbol_edges(sources: dict[str, SourceFile]) -> list[list[str]]:
    """The cohesion graph of one package: [symbol, symbol] for each top-level
    symbol whose declaration uses the name of another symbol of the package."""
    declared = {name for f in sources.values() for name in f.symbols}
    edges = {
        (name, used)
        for f in sources.values() for name, refs in f.symbol_refs.items() for used in refs & declared
    }
    return [list(edge) for edge in sorted(edges)]


def file_signals(
    source: dict[str, SourceFile],
    commits: list[Commit],
    file_pairs: Counter[tuple[str, str]],
    package_graph: dict[str, set[str]],
) -> tuple[dict[str, Signals], list[Signals], list[Signals]]:
    """Per-file signals (transactions is how many change transactions back its
    co-change numbers), the most frequent co-changing file pairs, and the
    highest-hotspot functions. A file's hotspot is its decayed change count
    times its complexity, a ranking key only. A function's hotspot is its
    complexity times the decayed change count of its file: git attributes
    changes to files, not functions. Size-dependent signals also come per
    line, so a large file does not top every list for being large."""
    decayed = decayed_changes(commits, source)
    churn: Counter[str] = Counter()
    changed: Counter[str] = Counter()
    reworked: Counter[str] = Counter()
    cards: dict[str, set[str]] = defaultdict(set)
    backing: Counter[str] = Counter()
    for t in transactions(commits):
        backing.update(p for p in t.files if p in source)
    for commit in commits:
        for path, deleted in commit.deleted_lines.items():
            if path not in source:
                continue
            churn[path] += 1
            changed[path] += commit.changed_lines[path]
            reworked[path] += deleted > 0
            if commit.card:
                cards[path].add(commit.card)

    pairs = {pair: n for pair, n in file_pairs.items() if n >= MIN_COCHANGES}
    hidden = {pair for pair in pairs if structurally_unlinked(*pair, package_graph)}
    out: dict[str, Signals] = {}
    for path, f in source.items():
        complexity = sum(fc.complexity for fc in f.functions)
        mine = {pair for pair in pairs if path in pair}
        lines = max(f.lines, 1)
        out[path] = {
            "churn": churn[path],
            "decayed_churn": round(decayed[path], 3),
            "relative_churn": round(changed[path] / lines, 3),
            "complexity": complexity,
            "complexity_per_loc": round(complexity / lines, 3),
            "hotspot": round(decayed[path] * complexity, 3),
            "hotspot_per_loc": round(decayed[path] * complexity / lines, 4),
            "loc": f.lines,
            "cards": len(cards[path]),
            "transactions": backing[path],
            "cochange_low_confidence": backing[path] < MIN_TRANSACTIONS_FOR_CONFIDENCE,
            "rework_rate": round(reworked[path] / churn[path], 3) if churn[path] else 0.0,
            "cochange_pairs": len(mine),
            "hidden_pairs": len(mine & hidden),
            "hidden_coupling_ratio": round(len(mine & hidden) / len(mine), 3) if mine else 0.0,
        }
    for rank, path in enumerate(sorted(out, key=lambda p: (-out[p]["hotspot"], p)), 1):
        out[path]["hotspot_rank"] = rank

    top_pairs = sorted(pairs, key=lambda pair: (-pairs[pair], pair))[:TOP_COCHANGE_PAIRS]
    cochange = [{"a": a, "b": b, "count": pairs[a, b], "hidden": (a, b) in hidden,
         "low_confidence": pairs[a, b] < MIN_TRANSACTIONS_FOR_CONFIDENCE} for a, b in top_pairs]

    functions = [
        {"file": path, "name": fc.name, "complexity": fc.complexity, "hotspot": round(decayed[path] * fc.complexity, 3)}
        for path, f in source.items() for fc in f.functions
    ]
    functions.sort(key=lambda fn: (-fn["hotspot"], fn["file"], fn["name"]))
    return out, cochange, functions[:TOP_HOTSPOT_FUNCTIONS]
