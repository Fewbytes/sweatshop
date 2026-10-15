"""The tree-sitter substrate: one parse per file, and the facts every language
adapter needs read out of it by tables.

A language is a LanguageSpec (lang_go.py, lang_python.py, lang_typescript.py):
node-type sets for cognitive complexity, queries for imports, top-level
declarations and idioms. This module holds no language knowledge, so adding a
language is adding a table. Grammar versions are pinned by checksum in
requirements.txt and recorded in signals.json, so numbers are comparable
between two runs and not with other tools.
"""

import os
import re
from collections.abc import Callable
from dataclasses import dataclass
from functools import cache

from tree_sitter import Language, Node, Parser, Query, QueryCursor

SKIP_DIRS = {".git", "vendor", "testdata", "node_modules", "__pycache__", ".venv"}
ANONYMOUS_FUNCTION = "<anonymous>"
_QUOTES = "\"'`"
# Comments carry no tokens: a comment edit must not move the clone signal.
_COMMENT_TYPES = {"comment", "line_comment", "block_comment"}


# (importing file, import text) -> package directory of the same repo, or None
ImportResolver = Callable[[str, str], str | None]


def package_of(path: str) -> str:
    return os.path.dirname(path) or "."


def module_dir(module_dirs: set[str], path: str) -> str:
    """The directory of the innermost module containing repo-relative path."""
    return max((m for m in module_dirs if m == "" or path.startswith(m + "/")), key=len, default="")


@dataclass(frozen=True)
class Idiom:
    concern: str
    name: str
    query: str  # one pattern; every match counts once


@dataclass(frozen=True)
class LanguageSpec:
    name: str
    tables_tool: str  # the name under which version is recorded in signals.json
    version: str  # bump when a table change moves numbers without any code change
    grammar_package: str  # PyPI distribution of the pinned grammar
    grammar: Language
    extensions: tuple[str, ...]
    is_test: Callable[[str], bool]  # repo-relative path -> test file?
    module_marker: str  # file naming a module root, for dead-symbol scoping ("" = whole repo)
    repo_signal_prefix: str  # "" keeps a language's repo keys unprefixed, e.g. Go's baselines

    # (repo root, repo-relative paths of this language's files) -> how its imports resolve
    import_resolver: Callable[[str, set[str]], ImportResolver]

    # Cognitive complexity.
    function_types: frozenset[str]  # named functions; reported unless nested in another function
    anonymous_function_types: frozenset[str]  # nest their body; reported only at top level
    bound_function_parent: str  # node type naming an anonymous function ("" = none)
    nesting_types: frozenset[str]  # +1 plus the nesting level, and nest their children
    if_types: frozenset[str]  # nesting types that can follow an else, which then costs only the else
    else_field: str  # field of an if holding its else; "" when else is a flat_types node
    flat_types: frozenset[str]  # +1 each: else clauses, goto
    logical_operators: dict[str, frozenset[str]]  # node type -> operators; +1 per run of one operator

    # Queries; each captures @name or @path.
    import_query: str
    declaration_query: str
    not_dead: re.Pattern[str]  # declared names that are used without a textual reference
    identifier_types: frozenset[str]
    idioms: tuple[Idiom, ...]


@dataclass
class FunctionComplexity:
    name: str
    complexity: int


@dataclass
class SourceFile:
    path: str  # repo-relative
    is_test: bool
    imports: list[str]
    functions: list[FunctionComplexity]
    symbols: list[str]
    identifiers: list[str]
    idioms: dict[tuple[str, str], int]
    tokens: list[str]  # leaf tokens without comments, for clone windows


@cache
def _parser(grammar: Language) -> Parser:
    return Parser(grammar)


@cache
def _query(grammar: Language, source: str) -> Query:
    return Query(grammar, source)


def _text(node: Node) -> str:
    return node.text.decode("utf-8") if node.text is not None else ""


def _captured(spec: LanguageSpec, source: str, root: Node, capture: str) -> list[Node]:
    captures = QueryCursor(_query(spec.grammar, source)).captures(root)
    return sorted(captures.get(capture, []), key=lambda n: n.start_byte)


def _nodes(root: Node):
    """Every node under root, document order."""
    stack = [root]
    while stack:
        node = stack.pop()
        yield node
        stack.extend(reversed(node.children))


def _score(spec: LanguageSpec, function: Node) -> int:
    body = function.child_by_field_name("body")
    if body is None:
        return 0
    score = 0
    stack: list[tuple[Node, int, bool]] = [(body, 0, False)]  # node, nesting level, else-if link
    while stack:
        node, level, chained = stack.pop()
        child_level = level
        if node.type in spec.nesting_types:
            score += 0 if chained else 1 + level
            child_level = level + 1
        elif node.type in spec.flat_types:
            score += 1
            for child in node.children:
                is_else_if = child.type in spec.if_types
                stack.append((child, level - 1 if is_else_if else level, is_else_if))
            continue
        elif node.type in spec.logical_operators:
            operator = node.child_by_field_name("operator")
            if operator is not None and operator.type in spec.logical_operators[node.type]:
                parent = node.parent
                parent_operator = parent.child_by_field_name("operator") if parent is not None else None
                same_run = parent is not None and parent.type == node.type and parent_operator is not None and parent_operator.type == operator.type
                score += 0 if same_run else 1
        elif node.type in spec.function_types or node.type in spec.anonymous_function_types:
            child_level = level + 1
        else_child = node.child_by_field_name(spec.else_field) if spec.else_field and node.type in spec.if_types else None
        if else_child is not None:
            score += 1
        for child in node.children:
            is_else_if = else_child is not None and child == else_child and child.type in spec.if_types
            stack.append((child, level if is_else_if else child_level, is_else_if))
    return score


def _functions(spec: LanguageSpec, root: Node) -> list[FunctionComplexity]:
    """Cognitive complexity per outermost function: +1 per nesting structure
    plus its nesting level, +1 per flat structure and per run of one logical
    operator. Nested functions deepen the level without scoring."""
    out: list[FunctionComplexity] = []
    stack = [root]
    while stack:
        node = stack.pop()
        name: str | None = None
        if node.type in spec.function_types:
            if node.child_by_field_name("body") is None:
                continue  # a declaration without a body (overload, external)
            name = _text(node.child_by_field_name("name"))
        elif node.type in spec.anonymous_function_types:
            parent = node.parent
            bound = parent is not None and parent.type == spec.bound_function_parent
            name = _text(parent.child_by_field_name("name")) if bound else ANONYMOUS_FUNCTION
        if name is None:
            stack.extend(reversed(node.children))
        else:
            out.append(FunctionComplexity(name, _score(spec, node)))
    return out


def parse_source(spec: LanguageSpec, path: str, source: bytes) -> SourceFile:
    root = _parser(spec.grammar).parse(source).root_node
    nodes = list(_nodes(root))
    idioms: dict[tuple[str, str], int] = {}
    for idiom in spec.idioms:
        matches = QueryCursor(_query(spec.grammar, idiom.query)).matches(root)
        idioms[(idiom.concern, idiom.name)] = len(matches)
    return SourceFile(
        path=path,
        is_test=spec.is_test(path),
        imports=[_text(n).strip(_QUOTES) for n in _captured(spec, spec.import_query, root, "path")],
        functions=_functions(spec, root),
        symbols=[
            name for n in _captured(spec, spec.declaration_query, root, "name")
            if not spec.not_dead.match(name := _text(n))
        ],
        identifiers=[_text(n) for n in nodes if n.type in spec.identifier_types],
        idioms=idioms,
        tokens=[_text(n) for n in nodes if n.child_count == 0 and n.type not in _COMMENT_TYPES and n.start_byte < n.end_byte],
    )
