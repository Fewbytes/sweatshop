"""The per-language adapter over the tree-sitter substrate: the per-package
code-health signals of kaizen.md section 3, computed from the facts syntax.py
reads out of each file by the language's tables.

History (churn and co-change) is read from the revision the orchestrator
resolved while the tree is read from disk, so a before and an after
measurement of an edited tree differ only by the edit.
"""

import hashlib
import importlib.metadata
import math
import os
from collections import Counter, defaultdict

from adapter import AdapterResult, History, Signals, Unavailable
from file_signals import (
    MIN_COCHANGES, ViolationThresholds, cochanging_pairs, file_signals, lcom4, modularity_violations, symbol_edges,
)
from lsp import REQUEST_TIMEOUT_SECONDS, LspUnavailable
from lsp_signals import ServerFacts, measure_with_servers
from syntax import SKIP_DIRS, ImportResolver, LanguageSpec, SourceFile, package_of, parse_source

# Bump when a change to the thresholds below or to this module moves numbers
# without any code change: it marks the run as a new baseline. Changes to one
# language's tables bump that spec's own version.
SOURCE_ADAPTER_VERSION = "5"
CLONE_WINDOW_TOKENS = 50


def source_files(spec: LanguageSpec, root: str) -> dict[str, SourceFile]:
    files: dict[str, SourceFile] = {}
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            if not name.endswith(spec.extensions):
                continue
            path = os.path.relpath(os.path.join(dirpath, name), root)
            with open(os.path.join(root, path), "rb") as f:
                files[path] = parse_source(spec, path, f.read())
    return files


def module_dirs(spec: LanguageSpec, root: str) -> set[str]:
    """Directories (repo-relative, '' for the root) holding the language's module marker."""
    found = set()
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        if spec.module_marker in filenames:
            rel = os.path.relpath(dirpath, root)
            found.add("" if rel == "." else rel)
    return found


def import_graph(
    files: dict[str, SourceFile], resolve: ImportResolver
) -> dict[str, Counter[str]]:
    """Package directory -> package directories of the same repo it imports,
    each with the number of import statements that reach it."""
    graph: dict[str, Counter[str]] = defaultdict(Counter)
    for path, f in files.items():
        pkg = package_of(path)
        graph[pkg]
        for imp in f.imports:
            target = resolve(path, imp)
            if target and target != pkg:
                graph[pkg][target] += 1
    return graph


def cyclic_packages(graph: dict[str, Counter[str]]) -> tuple[int, set[str]]:
    """Tarjan's strongly connected components: (cycle count, packages in one)."""
    index: dict[str, int] = {}
    low: dict[str, int] = {}
    on_stack: set[str] = set()
    stack: list[str] = []
    cycles = 0
    members: set[str] = set()

    def visit(v: str) -> None:
        nonlocal cycles
        index[v] = low[v] = len(index)
        stack.append(v)
        on_stack.add(v)
        for w in sorted(graph[v]):
            if w not in index:
                visit(w)
                low[v] = min(low[v], low[w])
            elif w in on_stack:
                low[v] = min(low[v], index[w])
        if low[v] == index[v]:
            component = []
            while True:
                w = stack.pop()
                on_stack.discard(w)
                component.append(w)
                if w == v:
                    break
            if len(component) > 1:
                cycles += 1
                members.update(component)

    for v in sorted(graph):
        if v not in index:
            visit(v)
    return cycles, members


def module_edges(
    graph: dict[str, Counter[str]], cochange: Counter[tuple[str, str]], hidden: set[tuple[str, str]]
) -> list[Signals]:
    """One edge per package pair joined by an import (either way) or by at
    least MIN_COCHANGES shared commits. `hidden` is co-change with no import."""
    imports: Counter[tuple[str, str]] = Counter()
    for importer, targets in graph.items():
        for target, n in targets.items():
            imports[tuple(sorted((importer, target)))] += n
    together = {pair: n for pair, n in cochange.items() if n >= MIN_COCHANGES}
    return [
        {"a": a, "b": b, "imports": imports[a, b], "cochange": together.get((a, b), 0), "hidden": (a, b) in hidden}
        for a, b in sorted(imports.keys() | together.keys())
    ]


def clone_fragments(files: dict[str, SourceFile]) -> Counter[str]:
    """Duplicated code per package: runs of consecutive CLONE_WINDOW_TOKENS-token
    windows (comments and whitespace are not tokens) that occur in 2+ places."""
    windows: dict[str, list[tuple[str, int]]] = defaultdict(list)
    for path, f in files.items():
        if f.is_test:
            continue
        for i in range(len(f.tokens) - CLONE_WINDOW_TOKENS + 1):
            text = "\x00".join(f.tokens[i:i + CLONE_WINDOW_TOKENS])
            windows[hashlib.sha1(text.encode()).hexdigest()].append((path, i))
    duplicated: dict[str, set[int]] = defaultdict(set)
    for places in windows.values():
        if len(places) > 1:
            for path, i in places:
                duplicated[path].add(i)
    fragments: Counter[str] = Counter()
    for path, starts in duplicated.items():
        for i in starts:
            if i - 1 not in starts:
                fragments[package_of(path)] += 1
    return fragments


def typed_ratio(typed: int, boundary: int) -> float:
    """1.0 when there are no entry-point functions: nothing crosses the boundary untyped."""
    return round(typed / boundary, 3) if boundary else 1.0


def entropy(counts: list[int]) -> float:
    total = sum(counts)
    return 0.0 - sum(c / total * math.log2(c / total) for c in counts if c) if total else 0.0


def concern_entropies(spec: LanguageSpec, idioms: Counter[tuple[str, str]]) -> dict[str, float]:
    concerns = sorted({i.concern for i in spec.idioms})
    return {c: round(entropy([n for (cc, _), n in idioms.items() if cc == c]), 3) for c in concerns}


REPO_SIGNAL_NAMES = (
    "hotspot", "complexity", "hidden_coupling_ratio",
    "clones", "import_cycles", "dead_symbols", "pattern_entropy", "call_depth", "typed_boundary_ratio",
)


class SourceAdapter:
    """Measures the files of one language. Package signals are keyed by
    directory, so two languages sharing a directory is an error in measure.py;
    repo signals carry the spec's prefix so languages do not overwrite each
    other."""

    package_signals = (
        "hotspot", "churn", "complexity", "hidden_coupling_ratio", "lcom4",
        "clones", "import_cycles", "dead_symbols", "pattern_entropy", "call_depth", "typed_boundary_ratio",
    )

    def __init__(
        self,
        spec: LanguageSpec,
        request_timeout: float = REQUEST_TIMEOUT_SECONDS,
        violation_thresholds: ViolationThresholds = ViolationThresholds(),
    ):
        self.spec = spec
        self.violation_thresholds = violation_thresholds
        self.request_timeout = request_timeout
        self._server_versions: list[str] = []
        self.language = spec.name
        self.repo_signals = tuple(spec.repo_signal_prefix + s for s in REPO_SIGNAL_NAMES)

    def tool_versions(self) -> dict[str, str]:
        return {
            "tree-sitter": importlib.metadata.version("tree-sitter"),
            self.spec.grammar_package: importlib.metadata.version(self.spec.grammar_package),
            self.spec.tables_tool: self.spec.version,
            self.spec.lsp_command[0]: ", ".join(self._server_versions) or "unavailable",
            "kaizen-source-adapter": SOURCE_ADAPTER_VERSION,
        }

    def measure(self, root: str, history: History) -> AdapterResult:
        spec = self.spec
        files = source_files(spec, root)
        source = {p: f for p, f in files.items() if not f.is_test}
        packages = sorted({package_of(p) for p in files})
        modules = module_dirs(spec, root)
        graph = import_graph(files, spec.import_resolver(root, set(files)))
        cycles, cyclic = cyclic_packages(graph)

        churn: Counter[str] = Counter()
        for commit in history.commits:
            for p in commit.deleted_lines:
                if p in source:
                    churn[package_of(p)] += 1
        cochange, file_cochange = cochanging_pairs(history.commits, files)
        pairs = {pair for pair, n in cochange.items() if n >= MIN_COCHANGES}
        hidden = {(a, b) for a, b in pairs if b not in graph[a] and a not in graph[b]}

        fragments = clone_fragments(files)
        facts = ServerFacts()
        unavailable: Unavailable | None = None
        try:
            facts = measure_with_servers(spec, root, files, modules, self.request_timeout)
        except LspUnavailable as e:
            unavailable = Unavailable(f"{spec.lsp_command[0]}: {e}")
        self._server_versions = sorted(facts.server_versions)
        dead = facts.dead

        pkg_idioms: dict[str, Counter[tuple[str, str]]] = defaultdict(Counter)
        for path, f in source.items():
            pkg_idioms[package_of(path)].update(f.idioms)

        out_packages = {}
        for pkg in packages:
            funcs = sorted(
                (fc for p, f in source.items() if package_of(p) == pkg for fc in f.functions),
                key=lambda fc: (-fc.complexity, fc.name),
            )
            complexity = sum(fc.complexity for fc in funcs)
            mine = {pair for pair in pairs if pkg in pair}
            own = {p: f for p, f in source.items() if package_of(p) == pkg}
            out_packages[pkg] = {
                "purpose": next((own[p].purpose for p in sorted(own) if own[p].purpose), ""),
                "entry_points": sorted({n for f in own.values() for n in f.entry_points}),
                "imports": dict(sorted(graph[pkg].items())),
                "symbol_edges": symbol_edges(own),
                "files": sum(1 for p in source if package_of(p) == pkg),
                "churn": churn[pkg],
                "complexity": complexity,
                "hotspot": churn[pkg] * complexity,
                "cochange_pairs": len(mine),
                "hidden_pairs": len(mine & hidden),
                "hidden_coupling_ratio": round(len(mine & hidden) / len(mine), 3) if mine else 0.0,
                "lcom4": lcom4(own),
                "clones": fragments[pkg],
                "import_cycles": int(pkg in cyclic),
                "dead_symbols": unavailable or len(dead[pkg]),
                "dead_symbol_names": unavailable or sorted(dead[pkg])[:10],
                "call_depth": unavailable or facts.call_depth[pkg],
                "typed_boundary_ratio": unavailable or typed_ratio(facts.typed_functions[pkg], facts.boundary_functions[pkg]),
                "pattern_entropy": round(sum(concern_entropies(spec, pkg_idioms[pkg]).values()), 3),
                "top_functions": [{"name": fc.name, "complexity": fc.complexity} for fc in funcs[:3]],
            }
        ranked = sorted(out_packages, key=lambda p: (-out_packages[p]["hotspot"], p))
        for rank, pkg in enumerate(ranked, 1):
            out_packages[pkg]["hotspot_rank"] = rank

        repo_idioms: Counter[tuple[str, str]] = Counter()
        for c in pkg_idioms.values():
            repo_idioms.update(c)
        by_concern = concern_entropies(spec, repo_idioms)

        repo = {
            "packages": len(packages),
            "hotspot": sum(p["hotspot"] for p in out_packages.values()),
            "churn": sum(churn.values()),
            "complexity": sum(p["complexity"] for p in out_packages.values()),
            "cochange_pairs": len(pairs),
            "hidden_pairs": len(hidden),
            "hidden_coupling_ratio": round(len(hidden) / len(pairs), 3) if pairs else 0.0,
            "clones": sum(fragments.values()),
            "import_cycles": cycles,
            "dead_symbols": unavailable or sum(len(v) for v in dead.values()),
            "call_depth": unavailable or max(facts.call_depth.values(), default=0),
            "typed_boundary_ratio": unavailable or typed_ratio(sum(facts.typed_functions.values()), sum(facts.boundary_functions.values())),
            "pattern_entropy": round(sum(by_concern.values()), 3),
            "pattern_entropy_by_concern": by_concern,
        }
        prefix = spec.repo_signal_prefix
        file_signals_by_path, top_pairs, top_functions = file_signals(source, history.commits, file_cochange, graph)
        # Files that import but resolve to no package mean the resolver is not
        # working for this tree, so "no violations" would be a false all-clear.
        if any(f.imports for f in files.values()) and not any(graph.values()):
            violations: list[Signals] | Unavailable = Unavailable(f"no {spec.name} import resolved: dependencies unknown")
        else:
            violations = modularity_violations(source, history.commits, file_cochange, graph, self.violation_thresholds)
        return AdapterResult(
            packages=out_packages,
            repo={prefix + key: value for key, value in repo.items()},
            files=file_signals_by_path,
            cochange_pairs=top_pairs,
            hotspot_functions=top_functions,
            module_edges=module_edges(graph, cochange, hidden),
            modularity_violations=violations,
        )
