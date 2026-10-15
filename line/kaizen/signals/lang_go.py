"""The Go tables of the tree-sitter substrate (syntax.py)."""

import os
import re

import tree_sitter_go
from tree_sitter import Language

from syntax import SKIP_DIRS, Idiom, ImportResolver, LanguageSpec, module_dir, package_of

_STD_CALL = """(call_expression
  function: (selector_expression operand: (identifier) @pkg field: (field_identifier) @fn)
  (#eq? @pkg "%s") (#match? @fn "%s"))"""

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


def go_import_resolver(root: str, files: set[str]) -> ImportResolver:
    modules = module_paths(root)
    by_import_path = {}
    for pkg in {package_of(f) for f in files}:
        mod_dir = module_dir(set(modules), pkg + "/")
        rel = pkg[len(mod_dir):].lstrip("/") if mod_dir else pkg
        rel = "" if rel == "." else rel
        by_import_path[modules[mod_dir] + ("/" + rel if rel else "")] = pkg
    return lambda importer, imported: by_import_path.get(imported)


GO = LanguageSpec(
    name="go",
    tables_tool="kaizen-go-tables",
    version="2",
    grammar_package="tree-sitter-go",
    grammar=Language(tree_sitter_go.language()),
    extensions=(".go",),
    is_test=lambda path: path.endswith("_test.go"),
    module_marker="go.mod",
    repo_signal_prefix="",
    import_resolver=go_import_resolver,
    function_types=frozenset({"function_declaration", "method_declaration"}),
    anonymous_function_types=frozenset({"func_literal"}),
    bound_function_parent="",
    nesting_types=frozenset({
        "if_statement", "for_statement", "expression_switch_statement",
        "type_switch_statement", "select_statement",
    }),
    if_types=frozenset({"if_statement"}),
    else_field="alternative",
    flat_types=frozenset({"goto_statement"}),
    logical_operators={"binary_expression": frozenset({"&&", "||"})},
    import_query="(import_spec path: (_) @path)",
    # Methods are skipped because interfaces can call them without a textual reference.
    declaration_query="""
        (source_file [
          (function_declaration name: (identifier) @name)
          (type_declaration (type_spec name: (type_identifier) @name))
          (type_declaration (type_alias name: (type_identifier) @name))
          (var_declaration [
            (var_spec name: (identifier) @name)
            (var_spec_list (var_spec name: (identifier) @name))])
          (const_declaration (const_spec name: (identifier) @name))
        ])""",
    not_dead=re.compile(r"^(main|init|_|Test\w*|Benchmark\w*|Example\w*|Fuzz\w*)$"),
    identifier_types=frozenset({"identifier", "type_identifier", "field_identifier", "package_identifier", "label_name"}),
    # Pattern entropy is the Shannon entropy of the counts within one concern:
    # 0 when one idiom is used, up when the code base mixes them.
    idioms=(
        Idiom("error-creation", "errorf-wrap", """(call_expression
          function: (selector_expression operand: (identifier) @pkg field: (field_identifier) @fn)
          arguments: (argument_list . (interpreted_string_literal) @format)
          (#eq? @pkg "fmt") (#eq? @fn "Errorf") (#match? @format "%w"))"""),
        Idiom("error-creation", "errorf-plain", """(call_expression
          function: (selector_expression operand: (identifier) @pkg field: (field_identifier) @fn)
          arguments: (argument_list . (interpreted_string_literal) @format)
          (#eq? @pkg "fmt") (#eq? @fn "Errorf") (#not-match? @format "%w"))"""),
        Idiom("error-creation", "errors-new", _STD_CALL % ("errors", "^New$")),
        Idiom("error-creation", "panic", '(call_expression function: (identifier) @fn (#eq? @fn "panic"))'),
        Idiom("logging", "log-std", _STD_CALL % ("log", "^(Print|Fatal|Panic)")),
        Idiom("logging", "slog", _STD_CALL % ("slog", ".")),
        Idiom("logging", "stderr-print", """(call_expression
          function: (selector_expression operand: (identifier) @pkg field: (field_identifier) @fn)
          arguments: (argument_list . (selector_expression operand: (identifier) @stream field: (field_identifier) @stream_name))
          (#eq? @pkg "fmt") (#match? @fn "^Fprint") (#eq? @stream "os") (#eq? @stream_name "Stderr"))"""),
        Idiom("logging", "stdout-print", _STD_CALL % ("fmt", "^Print")),
        Idiom("logging", "logger-field", """
          (call_expression
            function: (selector_expression
              operand: (selector_expression field: (field_identifier) @logger)
              field: (field_identifier) @fn)
            (#eq? @logger "Log") (#match? @fn "^Print"))
          (call_expression function: (identifier) @fn (#eq? @fn "logf"))
          (call_expression function: (selector_expression field: (field_identifier) @fn) (#eq? @fn "logf"))"""),
    ),
)
