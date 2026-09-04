# bughunt dogfood 001 — agentsh

Date: 2026-09-04. Tool built from `bughunt` at the close of the `scan` plan
(`design-docs/plans/2026-08-23-bughunt-scan.md`). Target: the `agentsh` module in
this repository.

This is the first time bughunt has been pointed at real code. Its purpose is not
the finding count; it is to answer whether the findings are worth acting on, and
to name the bug classes nothing caught — that list is the input to the plan that
writes the first emitted rules.

## What ran

```
cd agentsh
export PATH="$(go env GOPATH)/bin:$PATH"
bughunt init && bughunt scan
```

The working directory matters. `bughunt scan` must run from inside a Go module —
see Defect 1. The repository root is not one; the modules are `agentsh/` and
`bughunt/`.

The PATH export matters too: `staticcheck`, `errcheck`, `ineffassign`, and
`gosec` install to `$(go env GOPATH)/bin`, which is not on the default PATH here.
Without it the scan silently runs with one detector instead of five.

| Detector | Status | Contribution |
|---|---|---|
| govet | ran | 0 findings |
| errcheck | ran | 66 findings |
| ineffassign | ran | 0 findings |
| gosec | ran | 40 findings |
| staticcheck | ran, but produced nothing usable | 0 — see Defect 3 |
| astgrep | skipped, unavailable | no rules exist yet — correct |

## Baseline

```
before the fix in this commit:   40 findings,  40 gating, exit 1
after:                          106 findings, 106 gating, exit 1
```

| Rule | Count |
|---|---|
| errcheck/unchecked | 66 |
| gosec/G115 integer overflow conversion | 15 |
| gosec/G104 errors unhandled | 12 |
| gosec/G304 file inclusion via variable | 6 |
| gosec/G204 subprocess launched with variable | 4 |
| gosec/G301 poor directory permissions | 1 |
| gosec/G202 SQL string concatenation | 1 |
| gosec/G118 (misc) | 1 |

The gate works: 106 findings, exit 1.

## Signal quality

Findings were read, not merely counted.

**`internal/storage/index.go:217`, `:218`, `:225`, `:226` — G115, genuine.**
`int64(binary.BigEndian.Uint64(header[8:16]))` converts values parsed from an
on-disk index header. A corrupt or hostile index encoding a uint64 above
`MaxInt64` produces a negative `Lines`, `Bytes`, or `Offset`, and those values
are used as counts and seek offsets. This is exactly the class G115 exists for,
on exactly the kind of input where it matters — file-format parsing. Worth a
bounds check.

**`internal/storage/store.go:66` — G104, true but low value.**
`store.Close()` is unchecked on a path that is already returning a more
important error. Correct as reported, but a developer would reasonably not act
on it.

**`internal/output/grep.go:143`, `cmd/agentsh/main.go:143`, `:509` — G204,
context-dependent.** Subprocess launched with a variable. This is what agentsh
*is* — a supervised command runner. These will be permanent residents unless
suppressed. They are the strongest argument in this report for per-rule
suppression with a recorded reason rather than blanket disabling.

**The 66 errcheck findings are better signal than they first appear.**
They are mostly `defer x.Close()` and `conn.Close()`, which reads like classic
lint noise. But four lines above the G104 above, `store.go:63` reads:

```go
if rows, pragmaErr := db.QueryContext(ctx, `PRAGMA busy_timeout=5000`); pragmaErr == nil {
    _ = rows.Close()
}
```

This codebase already marks deliberate ignores with `_ =`. errcheck does not
flag those. So the 66 findings are precisely the sites that deviate from the
project's own established convention — not a linter's opinion imposed from
outside. That reframes them from noise into a consistency backlog the codebase
already implicitly asked for.

The qualifier: **26 of the 66 are in `_test.go` files**, where an unchecked
`conn.Close()` matters much less. A severity split or a path-based rule for
tests would sharpen this considerably.

## Defects in bughunt that this run exposed

Four. Two of them are the kind that make a gate lie, which is worse than a gate
that is merely noisy.

### Defect 1 — a non-module directory passes silently. OPEN.

At the repository root:

```
$ bughunt scan
0 findings, 0 gating          # exit 0 — gate open
$ go vet ./...                # what it actually ran
pattern ./...: directory prefix . does not contain main module
exit 1
```

`go vet` failed outright. Its error matches no diagnostic pattern, so it yields
zero hits, and bughunt reports success with five detectors "available". A
detector that never ran is indistinguishable from a clean run.

This is the worst failure mode a commit gate has: it is green, and it checked
nothing. Anyone running bughunt at the root of this monorepo gets that result
today.

**Fix requires** the `Detector` contract to distinguish "ran clean" from "failed
to run" — an exit code with no parseable output and non-empty stderr is the
signal. That changes every adapter and the run record, so it belongs in its own
task with its own review rather than bolted onto this one.

### Defect 2 — errcheck findings were silently discarded. FIXED IN THIS COMMIT.

errcheck separates location from message with a **tab**, not a space:

```
$ errcheck ./... | head -1 | od -c
.  g  o  :  5  0  8  :  1  7  :  \t  d  e  f  e  r
```

The shared regex required colon-space:

```go
`^(.+?):(\d+):(\d+): (.+)$`     // matched 0 of 5 sample lines
`^(.+?):(\d+):(\d+):[ \t]+(.+)$` // matches 5 of 5
```

All 66 errcheck findings vanished. The unit tests could not catch this: they
replay a hand-written fixture that used a space, so the fixture encoded the same
wrong assumption as the code.

Fixed here, with a regression test using errcheck's real tab-separated output.
Recovery is exact: 40 → 106 findings, and the 66 recovered match errcheck's own
count, so the fix recovers everything and invents nothing.

### Defect 3 — staticcheck is silently broken here. OPEN.

```
-: internal error in importing "internal/cpu" (export data version 4 is greater
   than maximum supported version 2); please report an issue (compile)
```

The installed staticcheck cannot decode go1.27 export data. It emits only these
lines, none match the diagnostic pattern, and bughunt reports staticcheck as a
healthy detector that found nothing. Same class as Defect 1, different cause.
A version-incompatible detector should be loudly unavailable, not quietly empty.

### Defect 4 — gosec's absolute paths break excludes and diff mode. OPEN.

gosec reports absolute paths while errcheck reports repo-relative ones:

```
! /Users/avishai/code/sweatshop/agentsh/internal/storage/index.go:226 [gosec/G115] ...
! cmd/agentsh/main.go:508 [errcheck/unchecked] ...
```

Two consequences follow by construction:

- `config.Excluded` matches patterns like `vendor/**` against `Hit.File`. An
  absolute path never matches, so exclude patterns are silently ineffective for
  every gosec finding.
- `scan.inDiff` compares `Hit.File` against the keys of `ChangedLines`, which
  come from `+++ b/<path>` in git's output and are repo-relative. An absolute
  path never matches any key, so **gosec findings can never gate in `--diff`
  mode** — the pre-commit configuration is blind to all 40 of them.

Neither is covered by a test, because every adapter test uses a hand-written
fixture with the path shape its author assumed.

**Fix**: normalize `Hit.File` to a repo-relative path once, centrally, before
excludes, diff matching, and fingerprinting. Needs a test per adapter against
real tool output.

## Bug classes no detector caught

This section is the input to the next plan's first emitted rules.

**1. The known failing test.** `agentsh/internal/daemon`'s `TestBashOutputGrep`
fails on master, independent of this work, and no detector in the set says
anything about it. That is expected — it is a behavioral failure, not a
structural pattern, and no static rule would catch it. It is recorded here to
make the boundary explicit: static analysis finds shapes, not wrong answers.
The lesson doc for this class should say so, so nobody tries to mechanize it.

**2. The tab-vs-space assumption that produced Defect 2 is itself a bug class**
worth a rule: a regex parsing external tool output that hard-codes a single
space as a field separator. An ast-grep rule over `regexp.MustCompile` string
literals containing `): (` is plausible and cheap. This is the most direct
candidate in this report — a bug found by hand today, mechanizable tomorrow,
which is the entire thesis of the tool.

**3. Path-shape assumptions (Defect 4)** are a second, harder class: code that
compares a path from one source against a path from another without normalizing.
Hard to catch structurally; a lesson doc entry is the honest answer for now.

**4. Fixture-encoded assumptions.** Defects 2, 3, and 4 share one root cause:
every adapter is tested against a hand-written fixture, so the test and the code
encode the *same* guess about a real tool's output. This is not mechanizable as
an ast-grep rule — it is an argument for the fixture-repo test layer the spec
already names, running real binaries against planted bugs. That layer would have
caught all three.

## Verdict

**Would a developer act on these findings, or disable the gate?**

Act on them — but only after Defects 1 and 4 are fixed, and with the test-file
split for errcheck.

The reasoning: the G115 cluster in the index parser is a real robustness gap on
file-format parsing; the errcheck findings are deviations from the codebase's
own `_ =` convention rather than an outside opinion; and 106 findings on a
codebase this size is a backlog, not a flood. The G204 subprocess findings are
inherent to what agentsh does and need suppression-with-reason, which is exactly
the mechanism the spec already specifies.

What would get the gate disabled is not the noise level. It is Defect 1: a gate
that reports success because it never ran teaches developers within a week that
its green means nothing. That is the first thing to fix, ahead of any new
language or detector.

The spec's own criterion is that if dogfooding produces findings nobody acts on,
the design is wrong and must change before more languages are added. On this
evidence the design holds. The defects found are in the plumbing, not the idea.
