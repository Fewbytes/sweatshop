"""Pre-submit contract gate: flag a change that removes or alters a guard, a
default, a fallback branch or an error contract (kaizen.md, section 3).

Agent changes to validation, fallbacks and error handling predicted later
failures where complexity and size deltas did not (CodeThread, arXiv
2606.21804). The rules are tree-sitter tables per language (contract_rules.py).
Each changed file is read at the base revision and in the working tree; the
contracts of the base that the tree no longer has are the findings. Added
contracts are not findings. Contracts are pooled over all changed files, so
moving a guard to another file is no change, and compared as token
fingerprints, so formatting, comments and renames are no change either.
"""

import enum
import os
import re
import subprocess
from collections import Counter
from dataclasses import dataclass

from tree_sitter import Node

import measure
from contract_rules import RULESETS
from gate import changed_files
from syntax import ContractRuleSet, captured, parser_for

JUSTIFICATION_PREFIX = "Contract-change:"
_JUSTIFICATION = re.compile(rf"^{JUSTIFICATION_PREFIX}[ \t]*\S", re.MULTILINE)
_COMMENT_TYPES = {"comment", "line_comment", "block_comment"}


class CardKind(enum.Enum):
    REFACTOR = "refactor"  # labelled refactor, or from Kaizen: must not change behaviour
    BEHAVIOUR = "behaviour"  # a card whose purpose is to change behaviour
    OTHER = "other"  # unknown: the commit message must justify the change


@dataclass(frozen=True)
class RemovedContract:
    path: str  # where the base had it
    rule: str
    text: str


def ruleset_for(path: str) -> ContractRuleSet | None:
    return next((s for s in RULESETS if path.endswith(s.language.extensions)), None)


def fingerprint(ruleset: ContractRuleSet, node: Node) -> str:
    """The leaf tokens of node, comments dropped and identifiers abstracted."""
    tokens: list[str] = []
    stack = [node]
    while stack:
        n = stack.pop()
        if n.type in _COMMENT_TYPES:
            continue
        if n.child_count:
            stack.extend(reversed(n.children))
            continue
        text = n.text.decode("utf-8") if n.text is not None else ""
        abstract = n.type in ruleset.language.identifier_types and text not in ruleset.fixed_identifiers
        tokens.append("_" if abstract else text)
    return " ".join(tokens)


def contracts_in(ruleset: ContractRuleSet, source: bytes) -> list[tuple[str, str, str]]:
    """(rule name, fingerprint, source text) of every contract in a file."""
    root = parser_for(ruleset.language.grammar).parse(source).root_node
    found: list[tuple[str, str, str]] = []
    for rule in ruleset.rules:
        for node in captured(ruleset.language, rule.query, root, "contract"):
            text = node.text.decode("utf-8") if node.text is not None else ""
            found.append((rule.name, fingerprint(ruleset, node), text))
    return found


def _base_source(root: str, base: str, path: str) -> bytes:
    shown = subprocess.run(["git", "-C", root, "show", f"{base}:{path}"], capture_output=True)
    return shown.stdout if shown.returncode == 0 else b""  # not in base: a new file


def _tree_source(root: str, path: str) -> bytes:
    try:
        with open(os.path.join(root, path), "rb") as f:
            return f.read()
    except FileNotFoundError:
        return b""  # deleted by the change


def removed_contracts(root: str, base: str) -> list[RemovedContract]:
    """Contracts the base revision had in a changed file that the working tree lacks."""
    before: Counter[tuple[str, str]] = Counter()
    after: Counter[tuple[str, str]] = Counter()
    seen: dict[tuple[str, str], list[RemovedContract]] = {}
    for path in changed_files(root, base):
        ruleset = ruleset_for(path)
        if ruleset is None:
            continue
        for rule, fp, text in contracts_in(ruleset, _base_source(root, base, path)):
            before[rule, fp] += 1
            seen.setdefault((rule, fp), []).append(RemovedContract(path, rule, text))
        for rule, fp, _ in contracts_in(ruleset, _tree_source(root, path)):
            after[rule, fp] += 1
    removed: list[RemovedContract] = []
    for key, count in sorted((before - after).items()):
        removed.extend(seen[key][-count:])
    return removed


def judge(removed: list[RemovedContract], kind: CardKind, message: str) -> list[str]:
    """Why the gate fails, empty when it passes. A refactor card may not remove
    or alter a contract at all; a behaviour card may; any other card must say why."""
    if not removed:
        return []
    if kind is CardKind.REFACTOR:
        return ["a refactor card must not change guards, defaults, fallbacks or error contracts"]
    if kind is CardKind.OTHER and not _JUSTIFICATION.search(message):
        return [f"justify the change in the commit message with a '{JUSTIFICATION_PREFIX} <why>' line, or make this a behaviour card"]
    return []
