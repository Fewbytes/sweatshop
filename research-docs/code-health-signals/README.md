# Code-Health Signals for Kaizen: Prior Art and Tool Evaluation

Research notes, 2026-10-15. Question: how should Kaizen measure code health
across several languages, which existing tools can it use, and which ideas
should it take from them?

Context: Kaizen's signals today are `line/kaizen/signals/*.py` (Go only, a
hand-written lexer, package level; cards `sweatshop-1x5`, `sweatshop-w60`).
The follow-up work is the epic `sweatshop-8t5` (signals v2), the line store
`sweatshop-xjk`, and the station tools `sweatshop-czu`. Background on what the
signals should mean is in
[`../llm-code-quality/README.md`](../llm-code-quality/README.md) and
[`line/guidance/kaizen.md`](../../line/guidance/kaizen.md).

Evidence grading as in the sibling note: **[strong]** replicated or
peer-reviewed, **[moderate]** single study or consistent preprints, **[weak]**
vendor material or self-description, **[run]** we ran it on this repo.

---

## 1. Summary

1. **Behavioral analysis (churn, change coupling, hotspots) is established
   practice** (Tornhill's code-maat and CodeScene; Mo, Cai and Kazman's
   hotspot patterns). Kaizen's hotspot, co-change and hidden-coupling signals
   are the same family. We should not invent new definitions; we should use
   theirs where they exist and cite them.
2. **No tool we examined covers Go behavioral and structural analysis
   together.** codelore, the most complete, has no Go support for complexity or
   imports **[run]**. Its git-history analyses are language-agnostic and usable.
3. **codemetrics is the best fit for per-function complexity in Go and 16 other
   languages.** It builds with `CGO_ENABLED=0` (pure-Go tree-sitter runtime), ran
   the whole repo in 0.2 s, and its totals match our lexer **[run]**.
4. **code-inspector is useful for ideas but not adoptable as is.** It needs
   cgo, and its Go import graph produced 0 edges on this repo **[run]**.
5. **arborist** is a Rust library with only complexity and SLOC. Nothing to
   adopt; a few API ideas.
6. **A finding about our own repo:** its history contains five committed build
   binaries, two versions each, 20 to 26 MB apiece (a 44 MB `.git` after
   compression; section 6). They broke codelore's ingest and need a guard.

Literature check ([`literature.md`](literature.md), 2026-10-15) changes some of
this: (a) our "hidden-coupling ratio" (pairs with no import edge over all
co-change pairs) is high by construction, since most co-change is non-structural;
use Mo et al.'s modularity violation (no dependency either way and co-change
above 2) as a ranked, trended list instead; (b) no source defines
"churn x complexity over 90 days": the best-supported score is Graves' decayed
change count, so state ours as our own choice; (c) size confounds nearly every
signal, so report per LOC or against size bands; (d) cohesion and cognitive
complexity are weaker predictors than `kaizen.md` implies, so rank and report,
do not gate; (e) Fisher-exact gating is codelore's idea, not from the
literature; (f) no study measures rework or churn of agent-written code.

Recommendation (detail in section 7): replace the hand-written Go lexer with
codemetrics' tree-sitter backend behind our adapter interface (`sweatshop-8t5.1`,
`.2`); keep our own git-derived signals but redefine them from the literature;
use codelore as a reference and as an optional external reader of git history,
not as a dependency.

---

## 2. What was evaluated and how

All four were cloned into a scratch directory (not executed from there) and
read; codemetrics, code-inspector and codelore were then run against a clean
clone of the line's `master` (`a4ebae3`) on `sweatshop-1`.

| Tool | Version run | How run | Licence |
|---|---|---|---|
| codelore | container `ghcr.io/emrecdr/codelore:latest` (crate 0.30.0) | podman, repo mounted | GPL-3.0-only |
| codemetrics | `@latest` (gotreesitter v0.55.0) | `go install`, `CGO_ENABLED=0` | MIT |
| code-inspector | `@latest` | `go install`, cgo | MIT |
| arborist | 0.1.3, read only | not run (Rust library, no CLI seen) | Apache-2.0 / MIT |

Versions are `latest` because this was exploration. Any adoption pins by version
and checksum (see `sweatshop-tj7`).

---

## 3. codelore

Rust drop-in successor to Adam Tornhill's code-maat. Mines git history into a
DuckDB fact store and runs a large catalogue of named analyses over it.
Pre-1.0, active (last commit 2026-10-14), SLSA provenance on releases. (The
clones for this note were shallow, depth 50, so commit counts for all four
tools are not totals and are left out.) **[weak]** for its own claims; what we ran is **[run]**.

**What it offers**

- Hotspots (`percentile(revisions) × percentile(cognitive) × (100 − health)`),
  hotspot velocity (accelerating files), function-level hotspots, function
  X-Ray, function-pair coupling with Fisher exact significance.
- Change coupling with a Fisher significance gate, coupling centrality,
  Leiden community detection on the coupling graph.
- Architecture: dependency cycles (Tarjan), cycle health (churn heat of each
  cycle), instability (Martin), propagation cost, Lakos ACD, architecture
  roles, `modularity-violations` (co-change with no import edge), unstable
  interface, crossing, god classes, architecture trend over sampled revisions,
  cycle origins (which commit created a cycle).
- Code health: biomarker composite (complex method, god class, large method,
  DRY, shotgun surgery, nesting, args, conditionals) banded red/yellow/green,
  health trend, refactoring-targets ROI, clones joined with change coupling
  ("live" duplicates), stale code.
- Knowledge and team analyses (ownership, bus factor, knowledge islands).
- Quality gate: `check`, `gate` (uncommitted changes against HEAD, meant for
  pre-commit and agent loops), `diff base..head` (PR mode), thresholds in a
  TOML file, SARIF out, GitHub Actions outputs.
- MCP server (11 tools) for agents; `explain <metric>` returns formula, citation
  and source SQL; SQL-queryable DuckDB store; 11 output formats; persistent
  cache; AI-attributed commit share (`ai-pct`); calibration against a reference
  corpus and against the repo's own defect history.

**What we found running it [run]**

- Language support is the blocker. Complexity and import extraction cover
  Rust, Python, Java, JavaScript and TypeScript. On this Go repo: "54 files have
  no complexity measurement", so `hotspots`, `code-health` and
  `dependency-cycles` returned 0 rows.
- Git-only analyses work for any language. `coupling` ranked file pairs by
  Fisher-significant co-change in under 0.5 s. The top pairs were
  implementation and its own test file (`cards.go` and `cards_test.go`,
  degree 100%), so test files need excluding or folding into their subject
  before the output means anything.
- `modularity-violations` is wrong without an import graph: it reports
  co-changing pairs as having no import edge only because no imports were
  resolved. Do not trust it for Go.
- Ingest failed on this repo: `blob 02b6445e... not found in object database`.
  The blob is a 20 MB committed binary (section 6). Using `--after 2026-10-11`
  to skip the history that holds it worked. The error message blames
  corruption; `git fsck` was clean.
- Runtime is not a concern (sub-second on 2,500 objects).

**Licence note.** GPL-3.0-only. Running it as an external program is fine.
Ideas, algorithms and published formulas are not copyrightable, so we may
re-implement any of its analyses, including with an LLM, as the owner
confirmed. The one thing to avoid is deriving our code from its source text:
a model that reads GPL code and reproduces it can yield a derivative work. So
the rule for any card that re-implements a codelore idea is: implement from
the cited papers and from the behaviour described in this note and its README
(the analysis catalogue, formulas and output columns), do not put codelore's
source in the implementing agent's context, and keep our own tests as the
specification. Cards `sweatshop-8t5.3` and `.10` carry this rule.

**Ideas to take**

- Gate on significance, not raw counts: Fisher exact on co-change pairs instead
  of our "at least 2 co-changes" threshold.
- Percentile ranks within the repo instead of absolute thresholds, and a
  calibration option against a reference corpus.
- Hotspot at function level (X-Ray), not only file or package.
- Fuse clones with change coupling: only duplicated code that also changes
  together is live debt.
- `gate`: measure the working tree against HEAD before submit. This is exactly
  what a Maker or Kaizen run needs before calling `sweatshop line merge`.
- Architecture trend and cycle origins: recompute at sampled historical
  revisions to see when decay started.
- `explain <metric>`: every signal carries its formula, citation and query. Our
  `signals.json` should too.
- An MCP or CLI surface so an agent can ask "what are the hotspots in this
  directory" while it works.
- Honest-limits discipline: it documents its known limitations and says when an
  analysis ran and matched nothing.
- Store design: normalized fact tables (commits, changes, hunks, entities,
  complexity, clones, imports, provenance) with analyses as SQL on top. This is
  a good model for `sweatshop-xjk`.

**Use for us.** Optional external reader for the language-agnostic history
analyses (coupling, hotspot velocity, ownership) in a container, behind our
adapter interface, never as a hard dependency. Re-evaluate Go support later.

---

## 4. codemetrics (github.com/richardwooding/codemetrics)

Go CLI and library: per-function cyclomatic and cognitive complexity, one pass.
Go via the standard `go/ast`, 16 other languages via tree-sitter
(`odvcencio/gotreesitter`, a pure-Go runtime). The root library package imports
only the standard library; tree-sitter is a separate subpackage. MIT. Recent
activity (last commit 2026-09-28).

**Run on this repo [run].** `CGO_ENABLED=0 go install` works. A 40 MB binary
analyzed the whole repo in 0.2 s: 2,127 functions including tests, 1,077
excluding test files, JSON output of `{file, function, cyclomatic, cognitive,
start_line, end_line}`.

**Agreement with our lexer [run].** On the same tree, our measurement gives a
repo complexity of 5,561 against codemetrics' cyclomatic total of 5,575 and
cognitive total of 5,832 (non-test code). So our "complexity" is
cyclomatic-like, and the two agree to 0.3%. For a function our script listed,
`makeCheckoutWorldWritable`, codemetrics reports cognitive 9, cyclomatic 6
against our 9. Our script reported two method names (`AttestBundle`,
`AttestLine`) that this tree no longer contains; that is the revision
difference, not a disagreement.

**Features worth having**

- Quality gate flags (`--max-cognitive`, `--max-cyclomatic`), baseline file
  (record today's debt, fail only on new findings; matched by rule, file and
  function, ignoring line numbers), `--diff base...head` to gate only touched
  functions, SARIF output.
- Respects `gocyclo:ignore` and `nolint:gocognit` style directives, plus its
  own `codemetrics:ignore`.
- Skips vendored and minified files via `projectdetect`.
- `Cognitive` is a pointer so "language reports no score" differs from zero.
  This is the same rule we set for unavailable signals in `8t5.1`.

**Limits.** Complexity only: no churn, no coupling, no imports, no clones.
Go analysis is `go/ast`, other languages tree-sitter, so cross-language numbers
share a definition (SonarSource cognitive complexity) but not an implementation.

**Use for us.** First choice for the complexity signal in every language it
supports. Wrap as an adapter; pin the version and record it in `signals.json`.

---

## 5. code-inspector and arborist

### code-inspector (github.com/miguelbravo7/code-inspector)

Go CLI and library; MIT; quiet since June 2026 (last commit 2026-06-21). Complexity
(cyclomatic, cognitive approximation, nesting, parameters, Halstead,
maintainability index), git churn, hotspot = complexity × churn,
token-normalised duplicate detection, import dependency graph with fan-in,
fan-out and cycles, TODO counts. 18 bundled languages, Go through `go/ast`,
Python and JS/TS hand-tuned, others via a generic adapter that derives hints
from the grammar's own node-kind vocabulary. It ships a Claude Code skill
(`.claude/skills/code-hotspots`) that runs the tool and proposes prioritized
refactorings.

**Run [run].** Needs cgo (built fine on the server with gcc). Ran in 0.5 s and
produced the metrics tree and summary: 371 files, 2,536 functions, about 51 k
lines, top hotspot `line/cmd/sweatshop/main.go` (cyclomatic 408 × churn 38).
**Its Go dependency graph returned 29 nodes and 0 edges**: with no `go.mod` at
the repo root and two modules (`line/`, `agentsh/`) it resolved no internal
imports. Hotspot ranking included `_test.go` files unless excluded.

**Ideas to take.** Grammar-derived node-kind classification for new languages
(less per-language tables); the shipped skill as a model for our
`sweatshop-czu` skills; a single ranked summary ("what should I fix first")
instead of a dozen unranked lists. **Not adoptable:** cgo, weaker Go graph.

### arborist (github.com/StrangeDaysTech/arborist, crate arborist-metrics)

Rust library, 0.1.3, 12 languages through tree-sitter feature flags, cognitive
and cyclomatic complexity and SLOC per function, JSON report, optional
threshold flag per function. About 8.6 k crates.io downloads, last commit 2026-05-19,
README states it was built with AI assistance. Complexity only; it duplicates
codemetrics for our purposes. **Ideas to take:** per-function threshold
reporting in the report type; the tier model for grammars (mature ones on by
default, the rest opt-in).

---

## 6. Finding: binaries in our git history

Asked while doing this work: why does the history contain binary blobs?

- Six large blobs, about 20 to 26 MB each, in `bin/agentsh`, `bin/agentshd`,
  `bin/bughunt`, `bin/depmaint` and `bin/sweatshop`, two versions each. The
  `.git` of a fresh clone is 44 MB, nearly all of it these.
- Cause: `bin/` was not in `.gitignore`. A Maker ran the build (it writes
  `bin/`), staged everything and committed the binaries: first in `60d8374`
  ("constitution: viceroy role...", maker-1, 2026-10-10), updated in `d01fcfd`
  ("line serve: cards API...", maker-1). The owner noticed and removed them in
  `32168c5` with a `.gitignore` entry, but removal does not drop them from
  history.
- Why the line allowed it: the merge guard checks the required `just ci` and
  path guards, but nothing rejects large or binary blobs, and Makers commit
  with a stage-all habit.
- Effects: codelore's ingest fails on the history; every clone (Maker, witness
  and Kaizen containers, GitHub) pays about 40 MB; history analyses are slowed.

**Proposed actions** (not yet done)

1. A guard in the merge guard (constitution): reject a commit that adds a blob
   over a size limit (for example 1 MB) or an executable binary outside an
   allow-list. Jidoka: fix the process, not only this instance.
2. Decide whether to rewrite history (`git filter-repo --path bin/ --invert-paths`).
   This changes every commit id after `60d8374`, needs a forced push to GitHub
   and a reseed of the line's `remote.git`, and invalidates the run history keyed
   by commit. It is a decision for the owner; a cheaper alternative is to leave
   history and use `--after` windows in analyses.

---

## 7. Recommendations for Kaizen signals v2

Maps onto the cards in `sweatshop-8t5`.

| Need | Recommendation | Card |
|---|---|---|
| Per-function complexity, many languages | Adapter around codemetrics, pinned and version-recorded | `8t5.1`, `8t5.2` |
| Retire hand-written Go lexer | Port after parity test; totals already agree to 0.3% | `8t5.2` |
| Imports and cycles | `go list` for Go; tree-sitter import queries elsewhere; do not use code-inspector's Go graph | `8t5.2` |
| Churn, co-change, hotspots | Own implementation from the literature; add Fisher significance, percentile ranks, function level, exclude tests (fold test into subject) | `8t5.3` |
| Hidden coupling | Co-change with significant p-value and no import edge; only valid once imports resolve for the language | `8t5.3` |
| Live clones | Join clones with change coupling | `8t5.3` |
| Pre-submit check | A `gate`-style measure of the working tree against HEAD | `8t5.1` |
| Explainability | Each signal carries formula, source and citation in `signals.json` | `8t5.1` |
| Storage | Fact-table design as in codelore; scores per revision | `sweatshop-xjk` |
| Module map in repo | Deterministic generated file per module, check in `just ci` | `8t5.6` |
| Agent-era replacement for authors | Cards per hotspot and rework rate | `8t5.3` |
| Binary-blob guard | Merge-guard size rule | new card needed |

Do not adopt: codelore as a dependency (language gap; GPL means run, never link), code-inspector
(cgo, weak Go graph), arborist (redundant, Rust).

---

## 8. Cards opened from this note

Filed 2026-10-15 on the sweatshop beads. Cards marked (owner) await approval or
a decision before the line will work on them.

| Card | What |
|---|---|
| `sweatshop-wd3` (owner) | Merge guard rejects large and executable blobs (P1) |
| `sweatshop-mnn` (owner) | Decision: rewrite history to drop the binaries, or leave it |
| `sweatshop-8t5.7` (owner) | Pre-submit gate: measure the working tree against HEAD |
| `sweatshop-8t5.8` (owner) | Validate that the signals predict anything on this repo |
| `sweatshop-8t5.9` (owner) | Architecture trend and cycle origins |
| `sweatshop-8t5.10` | Research follow-ups (other tools, cohesion, literature): section 9 |
| `sweatshop-8t5.1`, `.2`, `.3`, `sweatshop-xjk` | Existing cards, annotated with the findings above |

Papers behind the behavioral signals are summarized in
[`literature.md`](literature.md).

---

## 9. Follow-up research (card `sweatshop-8t5.10`)

Run 2026-10-15 on a clean clone of `master` (`e0f064f`: 274 Go files, 18
Python, 1 TypeScript, 9 shell). Tools installed into a scratch directory; all
**[run]** unless marked otherwise.

### 9.1 Complexity, size and clone tools

| Tool (version) | Reports | Go | Python | TypeScript | Shell | Repo time |
|---|---|---|---|---|---|---|
| codemetrics | per-function cyclomatic and cognitive | yes | yes | yes | **no** | 0.4 s |
| gocyclo 0.6.0 | cyclomatic | yes | no | no | no | 0.05 s |
| gocognit | cognitive | yes | no | no | no | 0.15 s |
| lizard 1.24.1 | cyclomatic, NLOC, tokens, params; cognitive with `-Ecognitive` | yes | yes | yes | **no** | 0.2 s (1.0 s with cognitive) |
| radon 6.0.1 | cyclomatic, Halstead, maintainability index, raw LOC | no | yes | no | no | 0.3 s |
| scc 3.7.0 | lines, code, comments, and a "Complexity" count | yes | yes | yes | yes | 0.1 s |
| tokei 12.1.2 | lines, code, comments (no complexity) | yes | yes | yes | yes | 0.05 s |
| jscpd 5.4.1 | token-based clones | yes | yes | yes | yes | 0.13 s |

(tokei 14 and 15 publish no Linux binary; 12.1.2 was the newest release with one.)

**Definitions, checked on a function whose answer we worked out by hand**
(`for`, `if` with `&& ... ||`, `else if`, `else`, `switch` with two cases,
and the equivalents in Python, TypeScript and shell):

- Go: Sonar cognitive complexity is 8 and cyclomatic is 8. codemetrics, gocyclo,
  gocognit and lizard (`-Ecognitive`) all give 8/8 (as applicable).
- Python: cognitive 7. codemetrics 7 and lizard 7. Cyclomatic differs by
  design: codemetrics 6, lizard and radon 8, because radon and lizard also count
  the comprehension and its `if`, and radon counts a `BoolOp` once. Pick one
  and record it; do not compare across tools.
- TypeScript: codemetrics folds a nested arrow function into the enclosing
  function (cyclomatic 7); lizard reports the arrow function separately (6 and 2).
- scc's "Complexity" is a count of branch keywords per file, not per function:
  14,331 for Go here against about 5,600 cyclomatic from the per-function tools.
  Useful as a file-level size proxy only; not Sonar and not McCabe.

**Agreement on this repo (non-test code).**

| Quantity | Value |
|---|---|
| Go cyclomatic total: codemetrics / gocyclo / lizard | 5,555 / 5,518 / 5,630 |
| Go cognitive total: codemetrics / gocognit / lizard | 5,807 / 5,808 / 5,655 |
| Python cyclomatic total: codemetrics / lizard / radon | 552 / 714 / 735 |
| Python cognitive total: codemetrics / lizard | 465 / 467 |
| Go functions: codemetrics / lizard | 1,053 / 1,171 (lizard lists closures) |

codemetrics and gocognit agree on Go cognitive to one point in 5,800. Lizard is
lexical, so on the largest functions it undercounts (e.g. `formatCargo`: 92
against 80); 37 of 456 matched Go functions differ. gocyclo is 37 below
codemetrics, which is plausibly closures and `select` cases, not checked per
function.

**Shell is not covered by any complexity tool here.** Only scc (keyword count)
and tokei/jscpd (size, clones) read shell. For shell, report size and
ShellCheck-style findings, not complexity.

**jscpd** found 50 clones, 594 duplicated Go lines (2.1%) in the 136 non-test Go
files and 7 lines (0.2%) in Python at `--min-lines 6 --min-tokens 60`; it is a
token matcher, so Type-1/2 clones only, with no similarity score for Type-3.

**Conclusion.** Keep codemetrics as the complexity source; cross-check with
gocognit for Go in the parity test (they agree to 1 in 5,800). Use lizard only
if a language codemetrics lacks is needed. Take line counts from scc or tokei
if the adapter does not already have them (scc is MIT, 0.1 s). jscpd is a
candidate clone source for `8t5.3`; none of the tools gives live clones.

### 9.2 CodeQL and Semgrep for call graphs

- **CodeQL CLI 2.27.2** downloads as a 416 MB zip in 7 s, and `codeql pack
  install` of `codeql/go-all` or `python-all` takes 13 s. Its libraries do model
  call targets (`CallExpr.getTarget()` in Go; points-to in Python), so a
  resolved call graph is a query away. **Database creation failed in this
  sandbox for both Go (`go build -a`, 40 s, tracer saw `go`, `cc1`, `ld`) and
  Python: "could not process any of it".** We did not find the cause, so the
  call-graph claim is from documentation, not our run **[weak]**. It also needs a
  build for Go, a GitHub-licensed CLI (free for open source only), and about
  40 s per Go database before any query.
- **Semgrep 1.181.0** (OSS) starts in 2 s but matches syntax: the pattern
  `Compute(...)` found 0 of the 9 textual mentions in Go because the calls are
  package-qualified (`cistats.Compute`), and nothing resolves a name to its
  definition. Cross-file resolution is a paid product. It cannot supply a call
  graph.
- **Conclusion.** Neither is a cheap source of references or call depth. The
  language servers below answer the same questions, run unchanged on a fresh
  checkout, and are already wired in (`kaizen/signals/lsp.py`). Revisit CodeQL
  only for data flow, which no signal needs yet.

### 9.3 Language servers on a cold checkout

Fresh `git clone` per run, through our own `LspSession`: initialize, open one
file, first `textDocument/references` on a function, then repeat, then call
hierarchy. Times are wall clock.

| Server | initialize | first references | repeat | call hierarchy | Notes |
|---|---|---|---|---|---|
| gopls 0.24.0 | 0.02 s | 6.6 s (1.4 s with gopls cache warm) | 0.01 s | 0.01 s | module cache already downloaded; first request type-checks the package and its dependencies |
| pyright 1.1.414 | 0.2 s | 0.8 to 1.1 s | 0.03 s | 0.00 s | no setup |
| typescript-language-server 6.0.2 | 0.1 s | 2.1 s | 0.01 s | 0.25 s | needs `typescript` 5 in the workspace `node_modules`; with `typescript` 7.0.2 (no `tsserver.js`) it fails to start |

Costs a signal run must budget: 7 s for Go on a truly cold cache, under 3 s
elsewhere, then milliseconds per query. Two gaps to guard: the TypeScript
server's dependency on a compatible `typescript` install must be a recorded
precondition, and a failed start must stay `LspUnavailable`, not a number.

### 9.4 Cohesion for Go packages

Surveyed in `literature.md` section 9, which proposes a definition (package
declaration graph; connected components as split candidates, density as the
score, hub declarations reported with and without). No tool reports it. The
worked example for `kaizen.md` is still to write once `8t5` implements the graph
(`graphs.py` has the import graph, not the declaration graph).

### 9.5 Literature

Done in `literature.md` (sections 2 to 7 and 11). Still unread, after another
attempt: Tornhill's books (paywalled), Mo et al. WICSA 2015 (publisher and
author copies refuse), Gall et al. 2003 (no open copy). The three rework leads
were read as abstracts: arXiv 2601.20109 (1,210 agent bug-fix PRs, SonarQube
differential; churn only normalises counts), 2601.21276 (agents disregard reuse;
MSR 2026) and 2603.28592 (302.6k AI-authored commits; 22.7% of introduced issues
still present at the latest revision). Only the last tracks anything over time,
and it tracks static-analysis issues, not rewritten lines. The gap stands: no
study measures rework of agent code.

---

## 10. Open questions and future research

Not yet done; each becomes a card or a note when picked up.
- **Mutation testing tools:** gremlins, go-mutesting (Go), mutmut (Python),
  Stryker (TypeScript): speed, determinism, scoping to changed code (`8t5.5`).
- **Do the signals predict anything here?** codelore's `calibrate-defects`
  idea: mine our own fix-commit history (bug cards and the commits that closed
  them) and test whether hotspot rank or health band predicted where bugs
  landed. Without this, Kaizen optimises numbers that may not matter.
- **Rework as a first-class signal for agents:** share of lines rewritten soon
  after landing, per card, per station. Needs the line store.
- **Dashboards:** codelore's single-file interactive dashboard is a model for
  the S3 report; look at what makes its hotspot map readable.

---

## Sources

- codelore: https://github.com/emrecdr/codelore (README, `docs/`), docs.rs/crate/codelore
- codemetrics: https://github.com/richardwooding/codemetrics, https://pkg.go.dev/github.com/richardwooding/codemetrics
- code-inspector: https://github.com/miguelbravo7/code-inspector
- arborist: https://github.com/StrangeDaysTech/arborist, https://docs.rs/arborist-metrics
- CodeScene X-Ray: https://docs.enterprise.codescene.io/versions/3.6.2/guides/technical/xray.html
- code-maat: https://github.com/adamtornhill/code-maat
- SonarSource cognitive complexity specification (G. Ann Campbell)
