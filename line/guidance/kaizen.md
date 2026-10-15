# Kaizen Guide: Refactoring and Simplifying Code for LLM Readers

For agents whose job is continuous, small, behavior-preserving improvement of
an existing codebase. Rationale and evidence:
`research-docs/llm-code-quality/README.md`. Target state: the writer guide
(`line/guidance/writer.md`).

Kaizen is **many small safe steps**, not rewrites. Each change must leave the
code measurably better on a stated signal and provably unchanged in behavior.

---

## 1. Ground rules

1. **Behavior preservation is non-negotiable.** Refactor only code covered by
   tests that would catch a behavior change. No coverage → write
   characterization tests first, as their own commit, then refactor.
2. **One refactoring per change.** A rename, an extraction, an inlining, a
   dedup. Never mix refactoring with behavior changes or bug fixes. If you find
   a bug, file an issue; do not fix it in the refactor.
3. **Measure before and after.** Every change names the signal it improves
   (Section 3) and reports the before/after value.
4. **Stay small.** Diffs a reviewer reads in minutes. If a refactor needs to
   touch more than ~10 files or ~400 lines, split it or file a plan.
5. **Respect hot paths and public APIs.** Do not change exported signatures,
   wire formats, schemas or CLI flags without an explicit issue authorizing it.
6. **Do not churn.** Do not refactor code that is stable and rarely changed
   unless it blocks something. Prioritize by change frequency.
7. **Do not chase style.** Formatting and naming taste are not kaizen targets
   unless they hurt search or consistency (Section 2, P4/P5).

## 2. What to look for (priority order)

Prioritize by **hotspot score = churn × complexity**, using git history over
the last ~90 days. Within a hotspot, fix in this order:

**P1. Hidden coupling.** Files that co-change frequently with no static
dependency between them. Fix: make the link explicit (shared constant / type /
function), or co-locate them, or add a test that fails on divergence.

**P2. Duplicated knowledge.** Clones that encode the same rule, constant,
schema or protocol. Fix: single source of truth. *Do not* merge incidental
look-alikes that encode different knowledge; check whether the copies have
diverged on purpose before unifying.

**P3. Unverifiable claims.** Comments, docstrings or docs that state
invariants a type, assert or test could check; stale or contradicted comments.
Fix: encode as type/assert/test; delete the comment. Delete comments you
cannot verify as true.

**P4. Search-hostile code.** Dynamic identifiers, string dispatch, reflection,
implicit registration, ambiguous or colliding names, overloaded generic names
(`Manager`, `Util`, `Handle`, `data`, `process`). Fix: literal dispatch tables,
explicit registration, unique domain names.

**P5. Pattern sprawl.** Multiple idioms for one concern (error handling,
logging, config, retries, I/O). Fix: pick the dominant or best idiom, migrate
the minority, one concern per change. Record the canonical idiom where agents
will see it (repo conventions doc).

**P6. Over-fragmentation and needless indirection.** Single-use helpers that
add a hop without a meaningful name; pass-through layers; interfaces with one
implementation; wrappers that only rename; deep call chains. Fix: inline,
collapse layers. This is a valid simplification, not a regression.

**P7. Over-large, low-cohesion units.** Files or functions mixing unrelated
concerns, or too large to read in one pass. Fix: split *by cohesion* — things
that change together stay together.

**P8. Dead weight.** Unused exports, dead branches, commented-out code,
obsolete flags, compat shims past their removal condition, speculative
parameters. Fix: delete. Confirm unused with symbol index plus text search
(including string references and config).

**P9. Weak types and weak tests.** `any` / untyped maps at boundaries,
stringly-typed enums, primitive IDs; tests with weak assertions or low
mutation score. Fix: tighten types; strengthen assertions; add missing edge
cases.

**P10. Control-flow load.** Deep nesting, long boolean conditions, flag
arguments. Fix: guard clauses, named predicates, split by flag.

## 3. Signals to measure

Report the relevant ones per change. Tools: ctags / LSP for symbols, ast-grep
for structure, git log for history, clone detector, type checker, mutation
testing where available.

| Signal | Direction | How |
|---|---|---|
| Hidden-coupling ratio | ↓ | co-change pairs without static edge ÷ co-change pairs |
| Clone count (knowledge clones) | ↓ | clone detector, manually triaged |
| Pattern entropy per concern | ↓ | ast-grep count of distinct idioms |
| Dynamic-dispatch / reflection sites | ↓ | ast-grep |
| Name collisions | ↓ | symbol index: same name, unrelated symbols |
| Context closure of touched functions | ↓ | tokens in body + direct deps |
| Call depth entry→work | ↓ | call graph |
| Import cycles | ↓ to 0 | dependency graph |
| Cognitive Complexity | ↓ | linter |
| Dead symbols | ↓ | symbol index + reference search |
| Typed boundary ratio | ↑ | type checker / ast-grep |
| Mutation score | ↑ | mutation testing |
| Stale / unverifiable comments | ↓ | review, test-execution checks |
| Test runtime | ↓ or = | test runner |

Kaizen stations compute hidden-coupling ratio, clone count, pattern entropy,
import cycles, cognitive complexity, dead symbols and test runtime with
`line/kaizen/signals/` (`kaizen_signals.py`; see `design-docs/kaizen-agent.md`,
"Station v1"); the rest are measured by hand.

LOC is **not** a target. Fewer lines is good only when it comes from deletion
or deduplicating knowledge, not from clever compression.

## 4. Anti-goals: do not do these

- Do not split working code into many tiny functions or files to meet a
  line-count rule. It raises lookup cost for agent readers.
- Do not introduce abstractions (base classes, generics, plugin systems,
  strategy patterns) to merge code that only looks alike.
- Do not add DI, reflection, decorators or metaprogramming to "clean up".
- Do not add comments that narrate code. Do not add docstrings that repeat
  the signature.
- Do not rename for taste. Rename only to fix collisions, ambiguity or
  misleading names.
- Do not reformat files you are not otherwise changing.
- Do not combine "while I'm here" fixes into a refactor.
- Do not refactor without green tests before and after.

## 5. Procedure per change

1. **Select** a target from the hotspot list and a finding category (P1–P10).
2. **Check safety net.** Run tests touching the target. If coverage of the
   target behavior is missing, write characterization tests; commit
   separately.
3. **Record baseline** for the chosen signal(s).
4. **Apply one refactoring.** Use mechanical, tool-supported transformations
   (LSP rename, ast-grep rewrite) over hand edits where possible.
5. **Verify.** Type checker, linters, full relevant test suite. Behavior and
   public API unchanged.
6. **Re-measure** and confirm the signal moved in the right direction with no
   regression in others (especially call depth, context closure, test
   runtime).
7. **Commit** with a message naming the refactoring, the signal and
   before/after values, e.g.
   `refactor: unify retry backoff into retry.Policy (clones 4→1)`.
8. **File follow-ups** for anything found but out of scope (bugs, API
   changes, larger restructures).

## 6. Change report template

```
Target:    <path / symbol>
Category:  P<n> <name>
Hotspot:   churn=<n> complexity=<n>
Change:    <one refactoring, one line>
Signals:   <signal>: <before> → <after>
Safety:    <tests run>, <characterization tests added?>
Behavior:  unchanged (tests: <pass count>), public API unchanged
Follow-ups: <issue ids or none>
```
