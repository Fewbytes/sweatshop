"""File-level signals from git history and the tree-sitter facts: hotspots,
co-change, hidden coupling, cards per hotspot, rework rate and cohesion.

All of it is language-agnostic: it reads SourceFile facts and the package
import graph, never a grammar. Definitions are in kaizen.md section 3.
"""

from collections import Counter, defaultdict
from itertools import combinations

from adapter import Commit, Signals
from syntax import SourceFile, package_of

# A commit touching more packages or files than this is a bulk change (rename,
# format), not evidence that the files depend on each other.
MAX_PACKAGES_PER_COMMIT = 20
MAX_FILES_PER_COMMIT = 50
MIN_COCHANGES = 2
TOP_COCHANGE_PAIRS = 50
TOP_HOTSPOT_FUNCTIONS = 20


def cochanging_pairs(
    commits: list[Commit], files: dict[str, SourceFile]
) -> tuple[Counter[tuple[str, str]], Counter[tuple[str, str]]]:
    """(package pairs, file pairs) -> number of non-bulk commits touching both."""
    package_pairs: Counter[tuple[str, str]] = Counter()
    file_pairs: Counter[tuple[str, str]] = Counter()
    for commit in commits:
        touched = sorted(p for p in commit.deleted_lines if p in files)
        pkgs = sorted({package_of(p) for p in touched})
        if len(pkgs) > MAX_PACKAGES_PER_COMMIT:
            continue
        package_pairs.update(combinations(pkgs, 2))
        if len(touched) <= MAX_FILES_PER_COMMIT:
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
    """Per-file signals, the most frequent co-changing file pairs, and the
    highest-hotspot functions. A function's hotspot is its complexity times
    the churn of its file: git attributes changes to files, not functions."""
    churn: Counter[str] = Counter()
    reworked: Counter[str] = Counter()
    cards: dict[str, set[str]] = defaultdict(set)
    for commit in commits:
        for path, deleted in commit.deleted_lines.items():
            if path not in source:
                continue
            churn[path] += 1
            reworked[path] += deleted > 0
            if commit.card:
                cards[path].add(commit.card)

    pairs = {pair: n for pair, n in file_pairs.items() if n >= MIN_COCHANGES}
    hidden = {
        pair for pair in pairs
        if package_of(pair[0]) != package_of(pair[1])
        and package_of(pair[1]) not in package_graph[package_of(pair[0])]
        and package_of(pair[0]) not in package_graph[package_of(pair[1])]
    }
    out: dict[str, Signals] = {}
    for path, f in source.items():
        complexity = sum(fc.complexity for fc in f.functions)
        mine = {pair for pair in pairs if path in pair}
        out[path] = {
            "churn": churn[path],
            "complexity": complexity,
            "hotspot": churn[path] * complexity,
            "cards": len(cards[path]),
            "rework_rate": round(reworked[path] / churn[path], 3) if churn[path] else 0.0,
            "cochange_pairs": len(mine),
            "hidden_pairs": len(mine & hidden),
            "hidden_coupling_ratio": round(len(mine & hidden) / len(mine), 3) if mine else 0.0,
        }
    for rank, path in enumerate(sorted(out, key=lambda p: (-out[p]["hotspot"], p)), 1):
        out[path]["hotspot_rank"] = rank

    top_pairs = sorted(pairs, key=lambda pair: (-pairs[pair], pair))[:TOP_COCHANGE_PAIRS]
    cochange = [{"a": a, "b": b, "count": pairs[a, b], "hidden": (a, b) in hidden} for a, b in top_pairs]

    functions = [
        {"file": path, "name": fc.name, "complexity": fc.complexity, "hotspot": churn[path] * fc.complexity}
        for path, f in source.items() for fc in f.functions
    ]
    functions.sort(key=lambda fn: (-fn["hotspot"], fn["file"], fn["name"]))
    return out, cochange, functions[:TOP_HOTSPOT_FUNCTIONS]
