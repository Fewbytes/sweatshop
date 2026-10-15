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
   characterization tests first, as their own commit, then refactor. "Covered"
   is measured, not guessed: `kaizen_signals.py mutation` gives the mutation
   score of the target's package or files (Section 3). A low score means the
   tests would not notice most behavior changes, so write characterization
   tests first. An unavailable score (tool missing, wall-time cap hit) is not
   a low score: check coverage by hand. After measuring, record the result
   with `sweatshop line kaizen mutation --commit SHA --source kaizen --file
   mutation.json` (the line keeps tool, version, sample size and runtime so we
   learn whether the score means anything and what it costs); `sweatshop line
   kaizen mutation-trend` prints the history. Never gate on it.
   Changes to input validation, defaults, fallbacks, and error handling or
   exception contracts count as behavior changes and are not allowed in a
   refactor, even when tests stay green. In CodeThread (arXiv 2606.21804)
   such changes predicted later failures (OR 1.83, p=.011); complexity,
   Halstead and LOC deltas did not. Complexity deltas are for ranking only.
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

Prioritize by **hotspot score = decayed change count × complexity**, using git
history over the last ~90 days. No source defines this product or this window;
the window and the join with complexity (for ranking only) are our choice. The
literature supports churn as the predictor: Graves et al. 2000 (decayed change
count) and relative churn (Nagappan and Ball 2005).
Static complexity barely changes between releases, so churn does most of the
work. Size confounds most signals: report them per LOC or within size bands
(`research-docs/code-health-signals/literature.md`). Within a hotspot, fix in
this order:

**P1. Hidden coupling.** Files that co-change frequently with no static
dependency between them (the Modularity Violation of Mo et al.). Work from a
ranked list of such pairs, not a ratio: most co-change has no static link, so
the ratio is high by construction. Fix: make the link explicit (shared constant / type /
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
| Hidden-coupling pairs (ranked list, not a ratio) | fewer, top of list first | co-change pairs without static edge, ranked by shared commits |
| Cohesion (LCOM4 components per package); rank and report, never gate | ↓ to 1 | `lcom4` in signals.json, defined below |
| Cards per hotspot, rework rate | ↓ | `files` in signals.json, defined below |
| Clone count (knowledge clones) | ↓ | clone detector, manually triaged |
| Pattern entropy per concern | ↓ | ast-grep count of distinct idioms |
| Dynamic-dispatch / reflection sites | ↓ | ast-grep |
| Name collisions | ↓ | symbol index: same name, unrelated symbols |
| Context closure of touched functions | ↓ | tokens in body + direct deps |
| Call depth entry→work | ↓ | call graph |
| Import cycles | ↓ to 0 | dependency graph |
| Cognitive Complexity; rank and report, never gate | ↓ | linter |
| Dead symbols | ↓ | symbol index + reference search |
| Typed boundary ratio | ↑ | type checker / ast-grep |
| Mutation score | ↑ | `kaizen_signals.py mutation` on the target scope: gremlins (Go), mutmut (Python), Stryker (TypeScript); killed, survived, timed-out; unavailable when the wall-time cap is hit |
| Stale / unverifiable comments | ↓ | review, test-execution checks |
| Test runtime | ↓ or = | test runner |

Kaizen stations compute modularity violations, clone count, pattern entropy,
import cycles, cognitive complexity, dead symbols and test runtime with
`line/kaizen/signals/` (`kaizen_signals.py`; see `design-docs/kaizen-agent.md`,
"Station v1"); the rest are measured by hand.

File-level and function-level signals (`files`, `cochange_pairs`,
`hotspot_functions` in signals.json) work for every language, from git history
plus the import graph:

- **Decayed churn** (`decayed_churn`) = the sum over the file's commits in the
  window of exp(−α · age) · log(1 + lines changed), α = 0.75 per year, age in
  years before the measured revision. The decay and α are Graves et al. 2000
  (decayed change count). Weighting by the log of the change size is ours;
  the 1 keeps a one-line change from weighing nothing. Lines
  changed are lines added plus lines removed.
- **Relative churn** (`relative_churn`) = lines changed in the window ÷ the
  file's current lines (Nagappan and Ball 2005, M1). Packages report it too.
- **Hotspot** = decayed churn × the file's cognitive complexity, ranked per
  file (`hotspot_rank`). The product is our choice, used to rank only; no
  source defines it. A function's hotspot is its complexity × the decayed
  churn of its file, because git records changes per file.
- **Per LOC** (`loc`, `complexity_per_loc`, `hotspot_per_loc`; packages have
  `hotspot_per_loc`): size confounds almost every metric (El Emam et al. 2001;
  Gil and Lalouche 2017), so a large file would top every list for being
  large. Compare files by the per-LOC figure before concluding that one is a
  worse hotspot than another; `churn` and `hotspot` stay as counts.
- **Co-change pair**: two files changed together in at least 2 non-bulk commits
  (a commit touching over 20 packages or 50 files is bulk and ignored). A pair
  is **hidden** when the files are in different directories and neither
  directory imports the other; same-directory pairs are never hidden.
- **Modularity violation** (`modularity_violations` in signals.json, per
  language, ranked): a hidden pair with at least 5 shared commits (`support`)
  and `confidence` of at least 30%, where confidence is support over the
  commits of the pair's busier file (Zimmermann). The thresholds
  (`ViolationThresholds`) are code-maat's defaults, our choice rather than a
  validated cut-off. `last_card` is the newest shared commit naming a card.
  Most co-change has no import, so the share of hidden pairs is high by
  construction and is not a score; the ranked list is. A language whose
  imports resolve to nothing reports `unavailable` ("not measured"), never an
  empty list.
- **Clone pairs** (`clone_pairs` in signals.json, ranked): file pairs that
  share at least one 50-token window now, listed only when their history says
  something, because many clones are deliberate (Kapser and Godfrey 2008).
  `live` is true for a pair that co-changed in at least 2 commits while
  staying similar (`support`; `confidence` is support over the commits of the
  busier file; Mondal et al. 2014). `divergence` is `unreconciled` when a
  commit to one file alone lowered their shared windows and nothing restored
  them (Juergens et al. 2009: only inconsistent clones carry fault risk), and
  `late_propagation` when a later change to one file alone restored them while
  the other was left untouched (Barbour et al. 2013: the riskiest). `route` is
  `bug_card` for the latter and `review` for the former, never a unification: a
  difference may be deliberate. `age_commits` counts the history's commits
  since the pair first shared code (the whole window if it already did); only
  `refactor_candidate` pairs, at least 8 commits old (Kim et al. 2005), are
  worth unifying. History is the 90-day window, so a pair that diverged
  earlier shows no divergence.
- **Cards per hotspot** (`cards`): distinct `Bead:` trailers among the commits
  touching the file. **Rework rate**: share of those commits that delete or
  replace lines (lines the file already had). Many cards with a high rework
  rate means many separate changes edit one place with no owner of its design.
  Authors are not measured: with agents writing the code an author is a
  station name.
- **Cohesion** (`lcom4`, per package, lower is better, 1 is ideal): the number
  of connected components when the package's non-test files are linked
  whenever one uses a name the other declares at top level. Example: package
  `g` has `x.go` (`func X() { Y() }`), `y.go` (`func Y() {}`) and `z.go`
  (`func Z() {}`). x and y are linked, z references nothing and nothing
  references it, so `lcom4` = 2: z does not belong with the rest; move it or
  find what should use it. A package of one file is 1.

- **Module map** (`docs/modules.json`, committed): per package, a purpose
  line (first line of its doc comment or docstring), public entry points
  (what the language exports, first 12 plus the total), packages it imports
  and is imported by, `lcom4`, hotspot rank and the commit it was last
  measured at. `just ci` runs `modules-check` (through the kaizen Go test), which fails when the
  purpose, entry points, imports or `lcom4` of any package, or the list of
  packages, differ from the tree: change the code, then refresh the map with
  `kaizen_signals.py modules --signals signals.json --map docs/modules.json`.
  Hotspot rank and hidden-coupling ratio follow the git history, which every
  commit moves, so `modules` rewrites a module's entry only when a tree field
  changed, its rank moved by 5 or more, or its hidden-coupling ratio by 0.2 or
  more; other entries stay byte for byte and master is not churned.
- **Graph views** in the S3 report (`report.html`): the module dependency
  graph (solid edges are static imports weighted by import count, dashed red
  edges are co-change with no import, weighted by shared commits, so hidden
  coupling shows without an import edge) and per-module cohesion graphs
  (nodes are top-level symbols, edges are references between them, one colour
  per connected component).

- **Pre-submit gate**: `kaizen_signals.py gate --root . --base HEAD` measures
  the working tree and `--base` the same way and, for the files the change
  touched (modified or untracked) and their packages, prints complexity, import
  cycle, clone and hotspot-rank changes. It exits 1 when one grows past a
  threshold. `--thresholds FILE` (JSON; fields
  `max_complexity_increase`, `max_new_import_cycles`, `max_new_clones` all 0, and
  `max_hotspot_rank_rise`, unset: reported only) sets the limits. `--baseline FILE` is a JSON list of finding
  keys printed by the gate (`complexity:PATH`, `cycle:PKG`, `clones:PKG`,
  `hotspot:PATH`) accepted as existing debt, so only new findings fail. Run it after
  the change (Section 5, step 6); Makers may run it before committing.

Cohesion and cognitive complexity have weak evidence as predictors (LCOM3 and
LCOM4 were not fault predictors; cognitive complexity correlates with
comprehension time, not correctness). Use them to rank candidates and report
them; never treat a change in them as passing or failing a refactor.

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
5. **Verify** with the refactor gate, checked in this order, stopping at the
   first failure: it compiles; tests pass; the target issue is gone; no new
   analyzer issues; mutation score on the touched code does not drop. Public
   API and behavior (ground rule 1) unchanged. Unaided single-shot LLM
   refactors broke the build or tests in 32-48% of attempts (SANER 2025,
   arXiv 2502.02368), so run this feedback loop on every change. Renames and
   signature changes go only through LSP rename or ast-grep, never by hand:
   hand-made unused-parameter fixes broke callers in 9 of 10 cases.
6. **Re-measure** (`gate` for the touched files) and confirm the signal moved in the right direction with no
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
