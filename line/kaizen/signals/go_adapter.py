"""The Go adapter: the per-package code-health signals of kaizen.md section 3,
computed from Go source with the lexer in golang.py.

History (churn and co-change) is read from the revision the orchestrator
resolved while the tree is read from disk, so a before and an after
measurement of an edited tree differ only by the edit.
"""

import hashlib
import math
import os
from collections import Counter, defaultdict
from itertools import combinations

import golang
from adapter import AdapterResult, History

# Bump when a change to the lexer, the idiom table or the thresholds below
# moves numbers without any code change: it marks the run as a new baseline.
GO_ADAPTER_VERSION = "1"
# A commit touching more packages than this is a bulk change (rename, format),
# not evidence that the packages depend on each other.
MAX_PACKAGES_PER_COMMIT = 20
MIN_COCHANGES = 2
CLONE_WINDOW_LINES = 6
CLONE_MIN_CHARS = 120
SKIP_DIRS = {".git", "vendor", "testdata", "node_modules"}


def go_files(root: str) -> dict[str, golang.GoFile]:
    files: dict[str, golang.GoFile] = {}
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            if not name.endswith(".go"):
                continue
            path = os.path.relpath(os.path.join(dirpath, name), root)
            with open(os.path.join(root, path), encoding="utf-8") as f:
                files[path] = golang.parse(path, f.read())
    return files


def module_paths(root: str) -> dict[str, str]:
    """Module directory (repo-relative, '' for the root) -> module path."""
    modules = {}
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        if "go.mod" in filenames:
            with open(os.path.join(dirpath, "go.mod"), encoding="utf-8") as f:
                for line in f:
                    if line.startswith("module "):
                        rel = os.path.relpath(dirpath, root)
                        modules["" if rel == "." else rel] = line.split()[1]
                        break
    return modules


def module_dir(modules: dict[str, str], path: str) -> str:
    """The directory of the innermost module containing repo-relative path."""
    return max((m for m in modules if m == "" or path.startswith(m + "/")), key=len, default="")


def package_of(path: str) -> str:
    return os.path.dirname(path) or "."


def import_graph(files: dict[str, golang.GoFile], modules: dict[str, str]) -> dict[str, set[str]]:
    """Package directory -> package directories of the same repo it imports."""
    by_import_path = {}
    for pkg in {package_of(p) for p in files}:
        mod_dir = module_dir(modules, pkg + "/")
        rel = pkg[len(mod_dir):].lstrip("/") if mod_dir else pkg
        rel = "" if rel == "." else rel
        by_import_path[modules[mod_dir] + ("/" + rel if rel else "")] = pkg
    graph: dict[str, set[str]] = defaultdict(set)
    for path, f in files.items():
        pkg = package_of(path)
        graph[pkg]
        for imp in f.imports:
            target = by_import_path.get(imp)
            if target and target != pkg:
                graph[pkg].add(target)
    return graph


def cyclic_packages(graph: dict[str, set[str]]) -> tuple[int, set[str]]:
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


def clone_fragments(files: dict[str, golang.GoFile]) -> Counter[str]:
    """Duplicated code per package: runs of consecutive CLONE_WINDOW_LINES-line
    windows (blank and bracket-only lines ignored) that occur in 2+ places."""
    windows: dict[str, list[tuple[str, int]]] = defaultdict(list)
    for path, f in files.items():
        if f.is_test:
            continue
        lines = f.normalized_lines
        for i in range(len(lines) - CLONE_WINDOW_LINES + 1):
            text = "\n".join(lines[i:i + CLONE_WINDOW_LINES])
            if len(text) >= CLONE_MIN_CHARS:
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


def entropy(counts: list[int]) -> float:
    total = sum(counts)
    return -sum(c / total * math.log2(c / total) for c in counts if c) if total else 0.0


CONCERNS = sorted({i.concern for i in golang.IDIOMS})


def concern_entropies(idioms: Counter[tuple[str, str]]) -> dict[str, float]:
    return {c: round(entropy([n for (cc, _), n in idioms.items() if cc == c]), 3) for c in CONCERNS}



class GoAdapter:
    language = "go"
    package_signals = (
        "hotspot", "churn", "complexity", "hidden_coupling_ratio",
        "clones", "import_cycles", "dead_symbols", "pattern_entropy",
    )
    repo_signals = (
        "hotspot", "complexity", "hidden_coupling_ratio",
        "clones", "import_cycles", "dead_symbols", "pattern_entropy",
    )

    def tool_versions(self) -> dict[str, str]:
        return {"kaizen-go-adapter": GO_ADAPTER_VERSION}

    def measure(self, root: str, history: History) -> AdapterResult:
        files = go_files(root)
        source = {p: f for p, f in files.items() if not f.is_test}
        packages = sorted({package_of(p) for p in files})
        modules = module_paths(root)
        graph = import_graph(files, modules)
        cycles, cyclic = cyclic_packages(graph)

        churn: Counter[str] = Counter()
        cochange: Counter[tuple[str, str]] = Counter()
        for touched in history.commits:
            for p in touched:
                if p in source:
                    churn[package_of(p)] += 1
            pkgs = sorted({package_of(p) for p in touched if p in files})
            if 1 < len(pkgs) <= MAX_PACKAGES_PER_COMMIT:
                cochange.update(combinations(pkgs, 2))
        pairs = {pair for pair, n in cochange.items() if n >= MIN_COCHANGES}
        hidden = {(a, b) for a, b in pairs if b not in graph[a] and a not in graph[b]}

        fragments = clone_fragments(files)
        identifiers: dict[str, Counter[str]] = defaultdict(Counter)  # module dir -> name -> uses

        for path, f in files.items():
            identifiers[module_dir(modules, path)].update(f.identifiers)
        dead: dict[str, list[str]] = defaultdict(list)
        for path, f in source.items():
            for name in f.symbols:
                if identifiers[module_dir(modules, path)][name] == 1:
                    dead[package_of(path)].append(name)

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
            out_packages[pkg] = {
                "files": sum(1 for p in source if package_of(p) == pkg),
                "churn": churn[pkg],
                "complexity": complexity,
                "hotspot": churn[pkg] * complexity,
                "cochange_pairs": len(mine),
                "hidden_pairs": len(mine & hidden),
                "hidden_coupling_ratio": round(len(mine & hidden) / len(mine), 3) if mine else 0.0,
                "clones": fragments[pkg],
                "import_cycles": int(pkg in cyclic),
                "dead_symbols": len(dead[pkg]),
                "dead_symbol_names": sorted(dead[pkg])[:10],
                "pattern_entropy": round(sum(concern_entropies(pkg_idioms[pkg]).values()), 3),
                "top_functions": [{"name": fc.name, "complexity": fc.complexity} for fc in funcs[:3]],
            }
        ranked = sorted(out_packages, key=lambda p: (-out_packages[p]["hotspot"], p))
        for rank, pkg in enumerate(ranked, 1):
            out_packages[pkg]["hotspot_rank"] = rank

        repo_idioms: Counter[tuple[str, str]] = Counter()
        for c in pkg_idioms.values():
            repo_idioms.update(c)
        by_concern = concern_entropies(repo_idioms)

        return AdapterResult(
            packages=out_packages,
            repo={
                "packages": len(packages),
                "hotspot": sum(p["hotspot"] for p in out_packages.values()),
                "churn": sum(churn.values()),
                "complexity": sum(p["complexity"] for p in out_packages.values()),
                "cochange_pairs": len(pairs),
                "hidden_pairs": len(hidden),
                "hidden_coupling_ratio": round(len(hidden) / len(pairs), 3) if pairs else 0.0,
                "clones": sum(fragments.values()),
                "import_cycles": cycles,
                "dead_symbols": sum(len(v) for v in dead.values()),
                "pattern_entropy": round(sum(by_concern.values()), 3),
                "pattern_entropy_by_concern": by_concern,
            },
        )
