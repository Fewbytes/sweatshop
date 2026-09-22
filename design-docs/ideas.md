# Agent tooling — current scope

## In agentsh (daemon owns execution records + process state)

1. Bash — DONE. `command: string`. Built-in bash disabled.
2. BashOutput / BashHistory / BashReplay / BashState — per existing spec.
3. Per-command output formatters
   - Detect go test / pytest / jest / cargo / kubectl / terraform from command line.
   - Parse to structured result: pass counts as numbers, failures with relevant frames only.
   - Returned inside Bash result. No new tool, no new call shape.
4. Log structuring
   - Tier 1: grep/slice (done).
   - Tier 2: Drain3 template clustering + stack-trace collapse + level histogram.
     Runs as sidecar over existing socket RPC; agentshd degrades to tier 1 if absent.
   - Tier 3: baseline diff — this run's templates vs prior runs of same command;
     novel/rare first, known noise suppressed.
   - Derived structure in SQLite: (invocation_id, template_id, count, first_seen,
     last_seen, exemplar_offset). Raw stays in blobs.
5. Loop detection
   - Detect N near-identical failing invocations within a window.
   - Emit unprompted in the offending result, with prior invocation IDs.
   - Passive; no tool surface.
6. Service lifecycle
   - start/stop/status/logs over named background invocations.
   - Readiness predicate: port open | stdout regex | HTTP 200.
   - `logs <name> --since <cursor>`.

## Separate tools

1. Session note capture
   - `note` with required type: fact | friction | preference | bug.
   - Requires citation: invocation IDs or equivalent evidence refs. No citation → reject.
   - Append-only. Never enters a future context directly.
2. Curation / promotion pass
   - Async batch. Cluster notes, require recurrence across independent sessions.
   - Promote by type: fact → agentsh failure-time hint; friction → tool backlog;
     preference → CLAUDE.md; bug → issue tracker.
   - Decay: unconfirmed after N sessions → drop.
   - Each promoted lesson names the metric it claims to move.
3. Tool-gap detector
   - Mine history DB for recurring multi-command sequences across sessions.
   - Output = script in repo, human-reviewed. Not auto-registered as an MCP tool.
4. Semantic code navigation — CONDITIONAL

- outline / def / refs / callers.
- Build only with a failure hook (e.g. grep >50 hits appends `→ refs '<symbol>'`).
- Without the hook it gets forgotten under context pressure; skip.

5. Auto bug hunter — skill + agent
   - Assets: static-analysis tool set (per language), bug DB, lessons-learned docs.
   - Hunt session runs on demand, pre-commit hook, or CI:
     a. run static analysis;
     b. LLM review for classes static analysis can't reach.
   - Per bug found: record in DB (type, location, provenance, status), write failing
     test first (see it red), fix (see it green), then decide if test is worth keeping —
     delete if not.
   - Bugs in already-committed work → fix on separate branch, PR only with user consent;
     DB tracks fix status.
   - After each find, subagent updates the lessons-learned doc: how to spot this class,
     how to avoid it. If mechanizable, emit a new static-analysis check (semgrep /
     ast-grep rule, custom vet pass) so the same bug never needs an LLM again.
   - DB doubles as metrics: bug types, counts, time-to-fix, which checks were earned.
   - Open questions
     - Bug DB = beads, or separate? Beads gives sync/dedup for free; a bug record needs
       fields beads lacks (class, detector, rule emitted).
     - Overlaps `note` + curation pass above: lessons-learned is the same promote-on-
       recurrence loop with bug as the type. Build one mechanism, not two.
     - Pre-commit / CI gating needs a bounded, deterministic mode — LLM review is
       neither. Split: static checks gate, LLM review is advisory / async.
     - Ratchet risk: emitted checks accumulate noise. Needs a false-positive rate per
       rule and auto-retirement, same decay policy as lessons.

## Rejected / deferred

- Change journal + rollback — git is sufficient.
- Output redaction — existing tools cover it.
- Search engines for logs (Toshi / Zinc / Bluge) — ranked retrieval is the wrong
  primitive; aggregation over templates is the query shape.
- DuckDB/Parquet inside agentshd — cgo dependency; offline analytics only.
- Session tracing — sessions aren't a comparable population.

## Constraints

- agentsh ships as a single pure-Go binary. Sidecars optional, never link-time deps.
- Language choice is per-tool; Go applies to agentshd only.
- Any tool requiring proactive invocation with no triggering failure needs a
  SKILL.md slot or it will be forgotten.
