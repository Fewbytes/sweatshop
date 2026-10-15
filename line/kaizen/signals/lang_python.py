"""The Python tables of the tree-sitter substrate (syntax.py)."""

import posixpath
import re

import tree_sitter_python
from tree_sitter import Language

from syntax import Idiom, ImportResolver, LanguageSpec, package_of

def python_import_resolver(root: str, files: set[str]) -> ImportResolver:
    """A dotted name is a module file (pkg/mod.py, in package pkg) or a package
    directory (pkg). Relative imports resolve against the importing file's
    directory; absolute ones against that directory and each ancestor, nearest
    first, which covers script directories and src layouts alike."""
    dirs = {package_of(f) for f in files}

    def known(candidate: str) -> str | None:
        if candidate + ".py" in files:
            return package_of(candidate)
        return candidate if candidate in dirs else None

    def resolve(importer: str, imported: str) -> str | None:
        here = posixpath.dirname(importer) or "."
        dots = len(imported) - len(imported.lstrip("."))
        dotted = imported[dots:].replace(".", "/")
        if dots:
            base = posixpath.join(here, *[".."] * (dots - 1))
            return known(posixpath.normpath(posixpath.join(base, dotted)))
        base = here
        while True:
            found = known(posixpath.normpath(posixpath.join(base, dotted)))
            if found is not None or base == ".":
                return found
            base = posixpath.dirname(base) or "."

    return resolve


PYTHON = LanguageSpec(
    name="python",
    tables_tool="kaizen-python-tables",
    version="1",
    grammar_package="tree-sitter-python",
    grammar=Language(tree_sitter_python.language()),
    extensions=(".py",),
    is_test=lambda path: re.search(r"(^|/)(test_[^/]*|[^/]*_test|conftest)\.py$", path) is not None,
    module_marker="pyproject.toml",
    repo_signal_prefix="python.",
    import_resolver=python_import_resolver,
    function_types=frozenset({"function_definition"}),
    anonymous_function_types=frozenset({"lambda"}),
    bound_function_parent="",
    nesting_types=frozenset({
        "if_statement", "for_statement", "while_statement", "except_clause",
        "match_statement", "conditional_expression",
    }),
    if_types=frozenset(),
    else_field="",
    flat_types=frozenset({"elif_clause", "else_clause"}),
    logical_operators={"boolean_operator": frozenset({"and", "or"})},
    import_query="""[
        (import_statement name: (dotted_name) @path)
        (import_statement name: (aliased_import name: (dotted_name) @path))
        (import_from_statement module_name: [(dotted_name) (relative_import)] @path)]""",
    declaration_query="""
        (module [
          (function_definition name: (identifier) @name)
          (class_definition name: (identifier) @name)
          (expression_statement (assignment left: (identifier) @name))
          (decorated_definition definition: (function_definition name: (identifier) @name))
          (decorated_definition definition: (class_definition name: (identifier) @name))
        ])""",
    not_dead=re.compile(r"^(main|_|__\w+__|test_\w*|Test\w*)$"),
    identifier_types=frozenset({"identifier"}),
    idioms=(
        Idiom("error-creation", "raise", "(raise_statement (call))"),
        Idiom("error-creation", "assert", "(assert_statement)"),
        Idiom("error-creation", "sys-exit", """(call
          function: (attribute object: (identifier) @module attribute: (identifier) @fn)
          (#eq? @module "sys") (#eq? @fn "exit"))"""),
        Idiom("logging", "logging-module", """(call
          function: (attribute object: (identifier) @module)
          (#eq? @module "logging"))"""),
        Idiom("logging", "logger-method", """(call
          function: (attribute object: (identifier) @logger)
          (#match? @logger "^(log|logger|LOG|LOGGER)$"))"""),
        Idiom("logging", "print", '(call function: (identifier) @fn (#eq? @fn "print"))'),
    ),
)
