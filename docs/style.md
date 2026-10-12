# Coding Style Guide

Short rules for code in this repo, drawn from
[`research-docs/llm-code-quality/README.md`](../research-docs/llm-code-quality/README.md).
It agrees with the [writer guide](../line/guidance/writer.md) and the
[kaizen guide](../line/guidance/kaizen.md); where they give rationale, this
guide gives the checkable rule. Testing rules live in [testing.md](testing.md).

Most agents read this code by searching it, and copy the patterns they find.
So the rules favor three things: changes that tools can verify, code that
search can locate, and patterns worth copying. Each rule names how to check
it. A rule with no check is a candidate for a kaizen finding or a new check.

## 1. Verifiable

| Rule | Check |
|---|---|
| `go vet` and `gofmt` are clean. | `just lint` |
| Use precise types: enums or named types over strings and bools, newtypes for IDs and units. No `any` / `interface{}` / `map[string]any` across package boundaries. | `grep -rnwE "interface\{\}\|any"` on the diff |
| Parse at the boundary and pass validated types inward. | review |
| Never swallow errors: no `_ = f()` on a fallible call, no empty `if err != nil {}`, no default that hides a missing input. | `grep -rn '_ = '`; errcheck (not yet in `just lint`) |
| Every behavior change ships with a test that fails without it. | `just test` |
| An invariant lives in a type or assert before a comment. | review |

## 2. Locatable

| Rule | Check |
|---|---|
| Exported names are descriptive and unique in the repo. Search before adding one. | `grep -rnw Name` returns only the related symbol |
| Never build identifiers by string concatenation (routes, config keys, event names, flags). Write the full literal. | `grep -rnE '"[a-z.:/_-]+" *\+'` on the diff |
| No reflection, no `init()` registration, no global mutable state. Dispatch through a literal table that lists every target. | `grep -rnE 'reflect\.\|func init\(\)'` |
| Keep code that changes together in one place. Extract a helper only if it is reused, tested on its own, or has a name worth reading. | review |
| Two places that must change together share a constant, type or function, or a test fails when they diverge. | co-change pairs with no static edge (research §5.3) |
| No import cycles; keep fan-out small. | `go build ./...` |
| Test files sit next to the code they test (`x_test.go`). | `find` for `_test.go` outside the package dir |
| A file fits one read: under about 500 lines, or the repo norm. | `wc -l` |

## 3. Imitable

| Rule | Check |
|---|---|
| Follow the existing idiom for each concern (errors, logging, config, I/O, HTTP). Do not add a second one. | pattern entropy (research §5.6) |
| One source of truth for rules, constants, schemas and protocol details. | review |
| No copy-pasted block over about 5 lines that encodes the same knowledge. | clone detection |
| No unused parameters, options, interfaces with one implementation, or config for needs that do not exist yet. | `staticcheck` (not yet in `just lint`) |
| Delete what you replace: no dead or commented-out code. | review |

## 4. Comments and docs

- Comments say why, never what. A wrong comment misleads more than a missing
  one (Macke & Doyle 2024), so change the comment in the same commit as the
  code.
- Exported symbols get a doc comment stating contract: inputs, outputs, errors,
  side effects. Prefer an `Example` test to prose.
- No narration ("now we loop") and no task references ("added for X"). The git
  log holds history.
- Check: a doc or comment that names a symbol, flag or path must still match
  it. `grep` for the name after any rename.

## 5. Diffs

- One logical change per commit. Keep renames, moves and formatting apart from
  behavior changes.
- Spend readability effort on the review surface: public signatures, types,
  test names and test bodies. Test names state behavior
  (`TestRetryStopsAfterMaxAttempts`).

## Not covered

The research leaves some questions open: the best unit size for LLM readers,
the effect of stale comments on generation, and the effect of type annotations
in prompts (research §6). This guide does not set rules there. Formatting
beyond `gofmt` is also left out, because the evidence shows it matters only
through consistency.
