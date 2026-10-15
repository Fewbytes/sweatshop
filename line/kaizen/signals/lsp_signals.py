"""Signals read from a language server instead of from text: dead symbols by
real references, call depth from the entry points down the call hierarchy, and
the share of entry-point functions whose signatures are fully typed.

One server runs per module of the language (go.mod, pyproject.toml,
package.json), because that is the unit a server resolves references within.
"""

import os
from collections import defaultdict
from dataclasses import dataclass, field

from lsp import REQUEST_TIMEOUT_SECONDS, CallItem, LspSession, Position
from syntax import LanguageSpec, SourceFile, module_dir, package_of


@dataclass
class ServerFacts:
    dead: dict[str, list[str]] = field(default_factory=lambda: defaultdict(list))  # package -> symbols with no reference
    call_depth: dict[str, int] = field(default_factory=lambda: defaultdict(int))  # package -> deepest call chain from an entry point
    typed_functions: dict[str, int] = field(default_factory=lambda: defaultdict(int))  # package -> entry-point functions fully typed
    boundary_functions: dict[str, int] = field(default_factory=lambda: defaultdict(int))  # package -> entry-point functions
    server_versions: set[str] = field(default_factory=set)


def measure_with_servers(
    spec: LanguageSpec, root: str, files: dict[str, SourceFile], modules: set[str],
    timeout: float = REQUEST_TIMEOUT_SECONDS,
) -> ServerFacts:
    """Raises LspUnavailable when any server cannot start, index or answer in time."""
    facts = ServerFacts()
    by_module: dict[str, dict[str, SourceFile]] = defaultdict(dict)
    for path, f in files.items():
        by_module[module_dir(modules, path)][path] = f
    for module, module_files in sorted(by_module.items()):
        if all(f.is_test for f in module_files.values()):
            continue
        with LspSession(spec.lsp_command, os.path.join(root, module), spec.lsp_language_id, timeout) as session:
            facts.server_versions.add(session.server_version)
            for path in sorted(module_files):
                session.open_file(os.path.relpath(path, module or "."))
            _read_module(spec, session, module, module_files, facts)
    return facts


def _read_module(
    spec: LanguageSpec, session: LspSession, module: str, module_files: dict[str, SourceFile], facts: ServerFacts
) -> None:
    source = {p: f for p, f in module_files.items() if not f.is_test}

    def repo_path(item: CallItem) -> str:
        return os.path.normpath(os.path.join(module, item.path)) if item.path else ""

    # Depth below a function, memoized. A function met again while still being
    # explored is a cycle and adds nothing, so a cycle's members can get a
    # depth that depends on where the walk entered; the walk order is sorted
    # so the number is still the same on every run.
    depths: dict[tuple[str, Position], int] = {}
    exploring: set[tuple[str, Position]] = set()

    def depth_below(item: CallItem) -> int:
        key = (item.path, item.position)
        if key in depths:
            return depths[key]
        if key in exploring:
            return 0
        exploring.add(key)
        callees = sorted(
            {(repo_path(c), c.position.line, c.position.character): c for c in session.outgoing_calls(item) if repo_path(c) in source}.items()
        )
        depths[key] = max((1 + depth_below(c) for _, c in callees), default=0)
        exploring.discard(key)
        return depths[key]

    for path, f in sorted(source.items()):
        rel = os.path.relpath(path, module or ".")
        pkg = package_of(path)
        for d in f.declarations:
            at = Position(d.line, d.character)
            if session.reference_count(rel, at) == 0:
                facts.dead[pkg].append(d.name)
            if not d.exported:
                continue
            callables = session.prepare_call_hierarchy(rel, at)
            if not callables:
                continue
            facts.boundary_functions[pkg] += 1
            if not spec.untyped_signature.search(session.hover_signature(rel, at)):
                facts.typed_functions[pkg] += 1
            facts.call_depth[pkg] = max(facts.call_depth[pkg], max(depth_below(c) for c in callables))
