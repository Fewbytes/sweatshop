"""The TypeScript tables of the tree-sitter substrate (syntax.py). Only the
plain TypeScript grammar is loaded: .tsx files are not measured."""

import posixpath
import re

import tree_sitter_typescript
from tree_sitter import Language, Node

from syntax import Idiom, ImportResolver, LanguageSpec, package_of

def typescript_import_resolver(root: str, files: set[str]) -> ImportResolver:
    """Only relative specifiers resolve: './x' is the file x.ts or the
    directory x, whose package is the file's directory or x itself. A '.js'
    extension, as TypeScript's ESM resolution requires, names the .ts file.
    Bare specifiers and tsconfig path aliases are not followed."""
    stems = {posixpath.splitext(f)[0] for f in files}
    dirs = {package_of(f) for f in files}

    def resolve(importer: str, imported: str) -> str | None:
        if not imported.startswith("."):
            return None
        target = posixpath.normpath(posixpath.join(posixpath.dirname(importer), imported))
        if target in dirs:
            return target
        stem = posixpath.splitext(target)[0]
        return package_of(stem) if stem in stems else None

    return resolve


def _inside_export(node: Node) -> bool:
    while node.parent is not None:
        node = node.parent
        if node.type == "export_statement":
            return True
    return False


TYPESCRIPT = LanguageSpec(
    name="typescript",
    tables_tool="kaizen-typescript-tables",
    version="2",
    grammar_package="tree-sitter-typescript",
    grammar=Language(tree_sitter_typescript.language_typescript()),
    extensions=(".ts", ".mts", ".cts"),
    is_test=lambda path: re.search(r"\.(test|spec)\.[mc]?ts$", path) is not None,
    module_marker="package.json",
    repo_signal_prefix="typescript.",
    import_resolver=typescript_import_resolver,
    function_types=frozenset({"function_declaration", "generator_function_declaration", "method_definition"}),
    anonymous_function_types=frozenset({"arrow_function", "function_expression", "generator_function"}),
    bound_function_parent="variable_declarator",
    nesting_types=frozenset({
        "if_statement", "for_statement", "for_in_statement", "while_statement", "do_statement",
        "switch_statement", "catch_clause", "ternary_expression",
    }),
    if_types=frozenset({"if_statement"}),
    else_field="",
    flat_types=frozenset({"else_clause"}),
    logical_operators={"binary_expression": frozenset({"&&", "||", "??"})},
    import_query="""[
        (import_statement source: (string) @path)
        (export_statement source: (string) @path)]""",
    declaration_query="""
        (program [
          (function_declaration name: (identifier) @name)
          (class_declaration name: (type_identifier) @name)
          (interface_declaration name: (type_identifier) @name)
          (type_alias_declaration name: (type_identifier) @name)
          (enum_declaration name: (identifier) @name)
          (lexical_declaration (variable_declarator name: (identifier) @name))
          (export_statement declaration: [
            (function_declaration name: (identifier) @name)
            (class_declaration name: (type_identifier) @name)
            (interface_declaration name: (type_identifier) @name)
            (type_alias_declaration name: (type_identifier) @name)
            (enum_declaration name: (identifier) @name)
            (lexical_declaration (variable_declarator name: (identifier) @name))])
        ])""",
    is_exported=_inside_export,
    lsp_command=("typescript-language-server", "--stdio"),
    lsp_language_id="typescript",
    untyped_signature=re.compile(r"\bany\b"),
    not_dead=re.compile(r"^(main|_)$"),
    identifier_types=frozenset({
        "identifier", "type_identifier", "property_identifier",
        "shorthand_property_identifier", "shorthand_property_identifier_pattern",
    }),
    idioms=(
        Idiom("error-creation", "throw-new-error", """(throw_statement
          (new_expression constructor: (identifier) @class (#match? @class "Error$")))"""),
        Idiom("error-creation", "throw-other", """(throw_statement
          [(identifier) (call_expression) (string) (member_expression)])"""),
        Idiom("error-creation", "promise-reject", """(call_expression
          function: (member_expression object: (identifier) @class property: (property_identifier) @fn)
          (#eq? @class "Promise") (#eq? @fn "reject"))"""),
        Idiom("logging", "console", """(call_expression
          function: (member_expression object: (identifier) @object)
          (#eq? @object "console"))"""),
        Idiom("logging", "logger-method", """(call_expression
          function: (member_expression object: (identifier) @logger)
          (#match? @logger "^(log|logger|LOG|LOGGER)$"))"""),
        Idiom("logging", "process-stream", """(call_expression
          function: (member_expression
            object: (member_expression object: (identifier) @process property: (property_identifier) @stream)
            property: (property_identifier) @fn)
          (#eq? @process "process") (#match? @stream "^std(out|err)$") (#eq? @fn "write"))"""),
    ),
)
