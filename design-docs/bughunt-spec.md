# bughunt — automated bug hunter

Status: design. Date: 2026-08-22.

## Purpose

Find bugs, prove them with a failing test, fix them, and convert each one into a
permanent static check so the same class never needs an LLM again. Track every
finding in a shared database so triage verdicts and false-positive rates
accumulate across a team instead of being rediscovered per developer.

The compounding claim: bug found once by review → mechanized as a rule → found
by `scan` forever after, at zero token cost. Everything else in this spec exists
to serve that loop.

## Position in sweatshop

- **agentsh** — supervised execution. `bughunt` runs detectors through it, so raw
  detector output is retained as content-addressed blobs, out of context,
  replayable by invocation id. Soft dependency: if agentshd is absent, scans
  still run, raw output is not retained.
- **beads** — work queue. Findings that survive triage but are not fixed in the
  session are escalated to beads; the bead id is stored on the finding. Beads
  never sees raw detector hits, suppressed findings, or findings fixed in-session.
- **bughunt** — its own binary, its own Dolt database, its own release cadence.

## Non-goals

- Not a replacement for code review, tests, or CI linting already in place.
- No dynamic analysis in v1: no `go test -race`, no fuzzing, no sanitizers.
  Different cost class, different trigger cadence.
- Not an auto-merging fixer. Fixes to committed code require explicit consent
  before a PR is opened.
- Not a general lint aggregator. Findings that no one triages and no rule
  mechanizes are failure, not output.

## Commands

`scan` and `hunt` are separate verbs because one gates and the other cannot.

```
bughunt init                      # create .bughunt/, Dolt db, config
bughunt scan [--diff <base>]      # deterministic; exit 1 on new findings
bughunt hunt [--review] [--budget N]
bughunt learn --evidence <commit|patch> [--class <name>] [--describe ...]
bughunt list [--status ...]
bughunt show <fingerprint>
bughunt triage <fp> --confirm | --false-positive [--reason ...]
bughunt fix <fp> --record ...
bughunt rule add | validate | list | retire
bughunt escalate <fp>
bughunt stats
bughunt sync
```

### scan

Deterministic only. Runs configured detectors plus repo-local rules. Never calls
an LLM. Bounded runtime. Records a `run` row and upserts `finding` rows. Exits
non-zero when new findings appear. "New" means a fingerprint that is either
absent from the database or present with status `new` or `confirmed`. Findings
with status `fixed`, `unreproducible`, or `suppressed` never gate; `escalated`
does not gate either, since it is already tracked as work in beads.

`--diff <base>` filters to findings touching lines changed against `<base>`.
This is a filter over full results, not a different analysis — the database
still learns about findings outside the diff, they just do not gate.

This is the only verb safe for pre-commit hooks and CI gates.

### hunt

The agent entry point. Never gates, never blocks a commit. Workflow below.

## Hunt workflow

1. **Gather.** Run `scan`. With `--review`, additionally run LLM review scoped by
   default to the diff against merge-base. Whole-repo review is opt-in and
   budgeted — it is the only step whose cost scales with codebase size and whose
   yield decays sharply on re-runs. Review findings are recorded with
   `detector=review` and lower default confidence.

2. **Triage.** Each finding arrives with prior state attached: previously seen,
   previously triaged as false positive, previously escalated. The agent works
   only `new` and `confirmed` findings. Everything else must not be
   re-litigated; suppressing that re-litigation is the main thing the shared
   database buys.

3. **Reproduce before fixing.** Write a test that fails for the stated reason.
   Run it. See it fail. If it cannot be made to fail, the finding is
   `unreproducible` — do not fix it.

   `unreproducible` suppresses the finding (it will not re-surface and will not
   gate) and opens a false-positive record against the rule: why it fired, what
   the code actually does, whether the rule can be narrowed. A false positive is
   a finding about the detector, symmetric to a bug being a finding about the
   code, and it feeds the same learning loop.

   - Repo-emitted rule → narrow it now, re-validate in both directions, record.
   - Upstream detector rule (staticcheck, gosec, …) → cannot be edited. Record a
     scoped suppression with its reason. That reason is the lesson.
   - Rule `fp_count` crossing threshold or ratio flags it for narrowing or
     retirement.

4. **Fix.** Minimal change. Test goes green. Re-run scan: finding gone, no new
   findings introduced.

5. **Keep or drop the test.** Keep when it pins a real invariant not otherwise
   covered. Drop when it only asserts the absence of one specific typo — that is
   the emitted rule's job. Default when uncertain: keep. Decision recorded on the
   finding.

6. **Learn.** Run the learning procedure below against the finding, using the
   pre-fix worktree state from step 3 as evidence. This is the step the rest of
   the workflow exists to reach.

**Budget.** Default 10 findings per session, ordered by severity, then
confidence, then age. The remainder stay queued. Without a budget a hunt session
becomes an unbounded refactor.

**Branch policy.** Bug in uncommitted work → fix in place. Bug in already
committed work → separate branch off the merge-base, one branch per finding or
per class. PR opened only on explicit user consent. Branch name and PR URL
recorded on the finding, status tracked to merge.

## Learning procedure

One procedure, defined once here, reached by two entry points:

1. **External** — an agent or a human finds a bug by any means (production
   incident, code review, a test they wrote, sheer luck) and calls
   `bughunt learn` to codify it. No scan involved. This is the higher-yield
   entry point: it captures bugs the detectors cannot currently find, which is
   exactly the population worth mechanizing.
2. **Internal** — `hunt` reaches step 6 with a reproduced and fixed finding.

The procedure is identical from that point on.

**Evidence requirement.** Learning cannot proceed without access to the pre-fix
code. A rule that has never been run against the buggy version is unvalidated,
and an unvalidated rule is a false-positive factory. `learn` therefore requires
`--evidence`: a fix commit sha (the parent is checked out as the pre-fix state)
or a patch. `hunt` satisfies this implicitly — step 3 already established a
failing state. If evidence is missing or the pre-fix state cannot be
reconstructed, `learn` stops and says so rather than emitting a rule it cannot
check.

**Finding row.** Every learned bug gets a `finding`, including external ones —
`detector=external`, status `fixed` or `confirmed`. Rules trace to findings via
`rule.origin_finding`, and `stats` counts externally-found bugs as the direct
measure of what the detectors are missing.

**Steps.**

1. **Classify.** Name the bug class, not the instance. An existing class reuses
   its lesson document and rule family; a new one starts both.

2. **Mechanize, if possible.** Ask whether a static rule can catch this class.
   If yes, write an ast-grep rule.

3. **Validate in both directions.** Run the rule immediately. It must fire on
   the pre-fix code and must not fire on the fixed code. Both directions are
   required: direction one proves it catches the bug, direction two proves it
   is not matching everything. A rule failing either direction is narrowed and
   re-validated, or abandoned.

4. **Commit and register.** Validated rule goes to `.bughunt/rules/`, its
   provenance and origin finding into the database, and its origin case into a
   fixture repo as a permanent regression test.

5. **Write the lesson when mechanization fails.** If no rule is possible — or
   none survives validation — a subagent writes or updates
   `.bughunt/lessons/<class>.md`: how the class manifests, how to spot it in
   review, why it resists static detection. Record the reason on the finding;
   that record identifies which bug classes need better detectors.

   This applies to both entry points equally. Mechanize when you can, write
   prose when you cannot, never both for the same knowledge — when a rule
   exists, the lesson points at it rather than restating it, since mechanized
   knowledge should not become prose an LLM must re-read.

## Finding identity

Fingerprint = `rule_id` + enclosing symbol + hash of normalized match text.

Survives line shifts, reformatting, and relocation of a function within or
across files. Line number is stored for display only and is never part of
identity. A renamed enclosing symbol produces a new fingerprint; this is
accepted — renamed code deserves a fresh look.

Symbol resolution comes from tree-sitter (or ctags where a grammar is
unavailable). Files with no resolvable enclosing symbol fall back to file path
plus normalized match hash.

## Detectors

A detector is an adapter with a fixed contract:

- `available() bool` — is the underlying tool installed?
- `run(paths) []Hit`

`Hit` normalizes to `{rule_id, file, line, symbol, message, severity, raw_ref}`.
Adapters translate native output, preferring JSON output modes where the tool
offers them.

v1 ships Go adapters only: `go vet`, `staticcheck`, `errcheck`, `ineffassign`,
`gosec`, plus `ast-grep` as the engine for repo-emitted rules. ast-grep earns its
v1 slot despite the Go-only scope because it is the engine that must work across
all target languages later.

Rust, Python, JS/TS, and Java are added as new adapters with no schema change.
That is the test of whether the contract is right.

Missing tools are auto-detected: warn, record `detector_unavailable` on the run,
do not fail. A gate that fails because a linter is not installed gets disabled by
the first person it annoys.

## Rules

Repo-local rules live in `.bughunt/rules/*.yaml` and are committed. Rules must be
reviewable in a diff; a rule nobody can read is a rule nobody trusts. The
database holds metadata only: provenance (which finding birthed it), hit counts,
true/false positive counts, retirement status.

Rules work without the database — `scan` runs them from the files. The database
adds accounting.

Future (not v1): a curated per-language rule pack shipped with the binary, with
repo rules extending and overriding it. Deferred because curation is a process
that does not exist yet.

## Storage

Dolt, synced over `refs/dolt/data` on the git remote — the beads pattern. Shared
triage verdicts and shared false-positive rates are what make the loop compound
instead of each developer rediscovering the same noise. A passive JSONL export is
maintained for grep and diff readability; the export is never the source of
truth.

Turso/libSQL with a VCS-committed export was considered and rejected: concurrent
triage produces line-level conflicts in a file whose lines are not semantically
independent, which means hand-writing three-way merge over rows. Dolt merges at
cell level natively, and the hot columns (`status`, `fp_count`) have exactly the
concurrent-update shape that needs it.

No separate telemetry store. Raw per-hit output is detector stdout, which agentsh
already persists as blobs; the run row carries the invocation ids. Blob retention
is agentsh's policy — old raw output is garbage collected, findings survive
because they live in Dolt.

### Tables

`run` — id, started_at, commit_sha, mode, diff_base, detectors_used,
detectors_unavailable, counts, agentsh_invocation_ids.

`finding` — fingerprint (pk), rule_id, detector, file, line, symbol, message,
severity, confidence, status, first_seen_run, last_seen_run, seen_count,
test_path, test_kept, fix_commit, branch, pr_url, bead_id, triage_note.

Status values: `new`, `confirmed`, `fixed`, `unreproducible`, `suppressed`,
`escalated`.

`detector` names the adapter that produced the finding, plus two synthetic
values: `review` for LLM review findings, `external` for bugs reported through
`bughunt learn`.

`rule` — id, engine, path, origin_finding, created_at, hit_count, tp_count,
fp_count, status (`active` | `narrowing` | `retired`), retirement_reason.

`lesson` — class, doc_path, rule_ids, updated_at. A thin index over
`.bughunt/lessons/<class>.md`; the documents are truth, the table is lookup.

## Layout

```
.bughunt/
  config.yaml           # detector enable/disable, severity floor, path excludes
  rules/*.yaml          # emitted rules, committed
  lessons/<class>.md    # one file per bug class, committed
  dolt/                 # database
```

An empty `config.yaml` is valid: auto-detect everything, no excludes.

## Install and integration

`bughunt init` creates `.bughunt/`, initializes Dolt, writes a default config,
and detects available tools, reporting what is missing and how to install it.

**Pre-commit.** `bughunt scan --diff HEAD`, deterministic and fast. Opt-in during
init. Never `hunt` — an LLM in a pre-commit hook is how the hook gets deleted.

**CI.** Two jobs. A gating job runs `scan` against the merge-base. An advisory
job may run `hunt --review` and post results, but never blocks the build.

**agentsh discovery.** Probe the daemon socket. Present → run detectors through
it and record invocation ids. Absent → run detectors directly, record
`raw_ref=null`. Never a hard failure.

**beads discovery.** Probe for `bd` and a `.beads/` directory. Absent →
`escalate` reports that escalation is unavailable rather than failing the hunt.

**Skill.** `skills/bughunt/SKILL.md` drives the workflow so the agent does not
improvise the sequence, and so `hunt` is invoked at the right moments rather than
being forgotten under context pressure — the constraint already recorded in
`design-docs/ideas.md`.

## Testing

Three layers.

**Unit.** Adapter output parsing against captured detector output. Fingerprint
stability: same code reformatted, function moved, lines inserted above — all must
produce identical fingerprints; function renamed must produce a different one.

**Fixture repos.** Small repos with planted bugs of known classes, each with an
expected finding set. Assert detection (no false negatives), assert clean repos
produce no findings (no false positives), assert fingerprint stability across
commits within the fixture. This is the regression suite for detectors and
emitted rules alike; every emitted rule adds its origin case to a fixture.

**Dogfood.** This repository is Go. Run `bughunt` against agentsh from the first
working scan. The measures of success are: findings that reproduce, rules
emitted per bug found, and false-positive rate trending down. If dogfooding
produces findings nobody acts on, the design is wrong and should change before
more languages are added.

## Open questions

- False-positive threshold for automatic rule retirement: absolute count, ratio,
  or ratio with a minimum sample size.
- Whether `hunt --review` should be able to run without any deterministic
  findings, purely as exploratory review, or always as a supplement to `scan`.
- Escalation policy: which statuses auto-escalate to beads versus requiring an
  explicit `escalate` call.
