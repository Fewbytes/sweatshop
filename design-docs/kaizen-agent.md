# Kaizen agent (simplicity/cohesion station)

Status: design; station v1 implemented (see "Station v1"). Card: `sweatshop-a82`.
Parent specs: [`assembly-line-spec.md`](assembly-line-spec.md) (Kaizen role,
structure analyzer, test value analyzer, heijunka, guards, build order step 6),
[`assembly-line-principles.md`](assembly-line-principles.md) (P1, P3, P4, P5,
P6, P7, P11). Sibling design: [`gemba-agent.md`](gemba-agent.md).

## Why Kaizen exists

Makers are paid in features, and every feature adds code. Nothing in the line
pushes the other way: Andon guards integrity, Gemba guards fitness and test
meaning, but no role owns the question "is this codebase getting harder to
change?" Without it, volume, coupling and build time ratchet upward, every
later card costs more, and lost CAS races and context overload (muda, muri)
rise with it. Kaizen is the standing counterweight to feature pressure: the
goal family `simplicity / cohesion` in P5.

## Mandate

1. **Keep complexity at the setpoint.** The Kaizen health metric is the
   complexity index versus a constitution-defined setpoint. Kaizen works to
   hold the index at or below it, and says so with evidence when it cannot.
2. **Find the next improvement.** Continuously search for hotspots, shallow
   modules, tangled dependencies, dead or unused code, redundant or slow
   tests, and stale dependencies, then turn each into a small, concrete,
   independently landable card.
3. **Improve by small steps.** Kaizen means many small changes, each of which
   passes the same checks as any other. Large rewrites are decomposed into a
   card chain before they are offered.

Kaizen does not build features (Makers), stop the line (Andon, though like any
agent it may pull the cord with evidence, P6), or decide what tests mean
(Gemba). Where goals overlap it files a card or a request; it does not command
(P1).

## Outputs: cards only

Kaizen **never merges**. Its station token carries no merge route, so the
refactor itself is done by a Maker who claims the Kaizen card. This is the
central design choice and it has three reasons:

- *P5 balance.* If Kaizen could land code, the simplicity share would be
  satisfied by one agent's taste. As a card producer, its output is
  rate-limited by heijunka and consumed by whoever has capacity, like every
  other goal.
- *P3.* A merge is costly and irreversible; the fewer principals that can do
  it, the smaller the guard surface. The merge guard needs no Kaizen-specific
  rule.
- *P4/P8.* The Kaizen agent proposes and measures; a different agent
  implements. The structure analyzer then verifies the result mechanically,
  so the agent that claims improvement is never the one that grades it.

| Output | Shape | Consumer |
|---|---|---|
| Hotspot card | module/file, the metric that fired, before value, target value, link to analyzer evidence | Makers |
| Simplify/delete card | candidate (unused feature, dead code, redundant test) with usage or coverage evidence | Makers; Gemba lands any acceptance-test deletion |
| Decomposition chain | parent card plus ordered children, each sized for one change | Makers |
| Dependency card | stale, vulnerable or heavy dependency, from depmaint campaigns | Makers |
| Request card | asks Gemba to retire or merge a test; asks the king (via proposal card) to change a setpoint or a rule | Gemba; sovereign |
| Proposal card | constitution diff plus evidence (setpoint, thresholds, card budget) | sovereign or meta-rules |
| Andon evidence | when complexity growth breaks a build or check, with the commit range | andon guard decides |

Every Kaizen card has goal family `kaizen`, states its **acceptance as a
metric delta** ("coupling of `pkg/x` falls from 14 to at most 8; `just ci`
passes; no public symbol removed unless listed"), and links the analyzer
evidence by content-addressed reference. A card without a measurable
acceptance criterion is rejected by the card emitter, not by review.

Kaizen cards are executed under the Maker role and the normal path: attest
`just ci`, `sweatshop line merge`. The delta in the card is then re-measured
by the analyzer on master, which closes or reopens the loop (see
[Metrics](#metrics)).

## Inputs

### Structure analyzer (deterministic, line infrastructure)

From the spec: infers cohesion, coupling and modularity from the code graph;
measures volume as API surface versus internal complexity per module (deep
versus shallow modules); tracks trends per commit. For Kaizen it provides:

- the **code graph**: files, packages/modules, symbols, import and call
  edges, per language adapter (`go list` first, others later);
- per-module measures: fan-in, fan-out, cycles, cohesion, API surface to
  internal size ratio, churn from git history, size and complexity;
- per-commit trend series stored with the commit so a delta is a query over
  two trees, not a recollection;
- ranked **hotspots**: modules that are both complex and frequently changed
  (the expensive combination), and cycles introduced since the last window.

The analyzer is code, not an agent, and its outputs are findings with a closed
schema (`kind`, `module`, `metric`, `before`, `after`, `commit_range`,
`evidence_ref`, `fingerprint`) so agent prompts never contain free-form text
lifted from source. Source text is still untrusted data (comments and
identifiers can carry instructions); Kaizen reads it as quoted data and holds
no tool that content could drive beyond card-filing (P3).

### Complexity index versus setpoint

The **complexity index** is a scalar (and a per-module breakdown) composed of
the spec's components: dependencies, volume, build and test time, and
coupling. The constitution (`heijunka` and a `kaizen` table) defines:

- the weights and normalization of the components;
- the **setpoint** per component or overall, set a priori by the owner
  (P2); Kaizen may propose a change but cannot apply it;
- the **deficit** definition: index above setpoint over a rolling window.

Heijunka turns the deficit into Kaizen's target share and card-priority
multiplier. Kaizen reads the signal to decide whether to pull, vacation or
sleep (consumption is advisory). **Below setpoint, Kaizen sleeps**: no
make-work. Zero rate with a persisting flame is the intended steady state of a
healthy codebase.

### Test value analyzer (shared with Gemba)

Kaizen consumes delete/merge candidates (low mutation contribution, high
redundancy) and slow/flaky suites (muri). Anything touching acceptance paths
goes to Gemba as a request. Unit tests colocated with code are ordinary
Maker-editable code and appear in normal Kaizen cards.

### Other sources

- **depmaint campaigns** become card generators (spec): Kaizen may propose a
  campaign; the king approves only where the constitution says so.
- **bughunt rule statistics**: rules are code too; a rule with a high false
  positive rate is a Kaizen candidate.
- **Field analyzer** unused-feature findings, via Gemba cards (usage evidence
  for removal).
- **Line metrics**: lost-race rate, re-attest cost, check duration from the
  spend ledger (muda, mura, muri).

## Constitution and guard interactions

Kaizen has no special guard. It lives entirely inside existing mechanism, so
policy stays in config (P2):

- **Merge guard.** Not applicable to Kaizen directly: it holds no merge
  route. The Maker implementing a Kaizen card faces the usual evidence-gated
  merge. Optionally the constitution may add a **non-regression check** to
  required checks: the structure analyzer fails a candidate whose complexity
  index worsens beyond a tolerance relative to base without a linked feature
  card. Off in v1; it is a ratchet and ratchets deadlock easily (see
  [Failure modes](#failure-modes)).
- **Role-gated paths.** Kaizen cards may touch any ungated path via a Maker.
  Gated paths (`justfile`, `station/**`, acceptance) follow their own rules; a
  Kaizen card that needs them becomes a request to the gating role or owner
  (P1, P11).
- **Constitution.** The `kaizen` section (weights, setpoints, thresholds,
  card budget per window, minimum hotspot confidence) is sovereign-gated.
  Kaizen files **proposal cards** with evidence; inside meta-rule bounds
  (default: none) the config guard may auto-apply, otherwise the sovereign
  decides. Kaizen never amends its own setpoint, which is the one conflict of
  interest it must not hold.
- **Andon.** Evidence-gated like any agent. Kaizen pulls it only when the
  structure analyzer shows a merged commit broke a structural invariant the
  constitution calls blocking (e.g. a new import cycle across a declared
  layer boundary).
- **Heijunka.** Reads its signal; its health metric feeds it. No other
  coupling.
- **Dependency-closure re-attestation.** The structure analyzer's graph is the
  same one the merge guard needs for the deferred "re-attest only the
  transitively dependent targets" optimization. One graph, two consumers; the
  guard consumer must treat the graph as untrusted-until-attested like any
  analyzer output, so Kaizen's use cannot be a path to weaken the guard.

## Station and token capabilities

Station `kaizen`, role `kaizen`, its own key and bearer token (M1 station
model: the token selects the station, role, agent ID and key). Runs as a
container under `sweatshop line kaizen`, parallel to `line maker` and `line
andon`.

| Capability | Allowed | Notes |
|---|---|---|
| Read cards, repo, analyzer output | yes | repo mounted read-only; analyzer findings via station API |
| `POST /v1/cards`, comments | yes | goal family `kaizen` only; card-budget and fingerprint-dedup enforced on the host |
| File proposal / request cards | yes | typed, with required evidence fields |
| Claim cards | yes | only `kaizen` family cards for the analysis task itself |
| Pull andon | yes, with evidence | like any agent |
| `/attest`, `/merge`, git push | **no** | 403; nothing to land |
| Constitution write, gated paths | no | |
| Network, secrets | none beyond the station URL and the model credential | |

The token cannot do harm beyond spamming cards, which the host bounds with a
per-window budget (a constitution value) and dedup. This keeps the trust
surface smaller than a Maker's.

An optional **sandbox-scratch** capability is worth considering later: let
Kaizen run a refactor *speculatively* in its container, measure the delta with
the analyzer, and attach the diff (as a bundle, never as a merge) to the card
as a hint. That raises card quality (P8) without any merge power. Not v1.

## Metrics

Computed by line infrastructure from evidence, never from agent reports (P4).

**Heijunka health metric (Kaizen):**

| Metric | Definition | Direction |
|---|---|---|
| **Complexity index vs setpoint** | weighted composite of dependencies, volume, build/test time, coupling, divided by setpoint, over a rolling window | at or below 1.0 healthy; above is a deficit |

**Track record (not heijunka inputs):**

- *Delta realized*: for closed Kaizen cards, the analyzer's measured change on
  master versus the card's promised delta. Cards that land with no or negative
  improvement count against the Kaizen lineage.
- *Churn caused*: Kaizen cards that were reverted, reopened, or whose target
  module regressed within N commits.
- *Acceptance rate*: fraction of Kaizen cards that Makers claim and complete;
  persistent decline means the cards are unusable, not that Makers are lazy.
- *Spend per unit of index reduction*, from the spend ledger.
- *False hotspot rate*: cards closed by a Maker with a reasoned decline that
  evidence later supports.
- *Card age and backlog*: open Kaizen cards by age (a growing pile is itself
  a flow problem, P11).

Without enough history (dogfood start) the index reports "no trend" and
heijunka shifts nothing, rather than reading a fresh baseline as health.

## Failure modes

| Failure | Cause | Mitigation |
|---|---|---|
| **Make-work / churn** | Kaizen keeps filing cards at setpoint | sleep below setpoint; per-window card budget; every card needs a measured delta; realized-delta track record |
| **Goodhart on the index** | Makers or Kaizen game the metric (split files, hide edges in reflection, move code to untracked paths) | multiple independent components; analyzer adapters cover all tracked paths; periodic review of weights by proposal; metric audits sampled |
| **Over-abstraction** | pursuit of low coupling creates shallow wrapper modules | analyzer measures API surface to internal size, penalizing shallow modules; hotspot ranking prefers deep modules |
| **Risky refactor wins the debate** | a Kaizen card is large and breaks behavior | cards are small by construction; ordinary checks and Gemba's acceptance tests judge the result; Andon catches post-merge breakage |
| **Deleting what matters** | unused-feature or test-delete cards based on thin evidence | usage evidence required; acceptance-test deletion goes via Gemba; owner review of retirements in v1 |
| **Ratchet deadlock** | non-regression check blocks a needed feature | off by default; tolerance and an explicit feature-card waiver; blocked candidates become cards, not stalled agents (P11) |
| **Feature/Kaizen oscillation** | Makers add, Kaizen strips, repeat | heijunka damping and bounded shifts; per-module cooldown after a Kaizen card lands |
| **Prompt injection via source** | comments or identifiers aimed at the Kaizen agent | structured findings; source quoted as data; Kaizen holds only card tools (P3) |
| **Card flood starving Makers** | too many Kaizen cards crowd out features | card budget; heijunka priority multiplier is bounded; Makers may decline with reasons |
| **Analyzer wrong** | language adapter misparses; graph incomplete | analyzer is tested like any code; false-finding rate is attributed to the analyzer, not the agent; low-confidence findings are dropped mechanically |
| **Kaizen capture** | Makers pressure Kaizen to drop or inflate cards | requests are cards with evidence; Makers never hold kaizen credentials; different lineages |
| **Starved or absent Kaizen** | role sleeps while debt accumulates | flame minimum (P7); deficit wakes sleepers via the scheduler |
| **No signal** | analyzer or adapter missing for a language | metrics report "no signal"; Kaizen falls back to unscored judgment cards, clearly marked |

## Station v1 (implemented: `sweatshop line kaizen`)

Card `sweatshop-sxt`. The station API has no card-creation route for Kaizen
yet (creation is viceroy-only), so v1 departs from "cards only": a Kaizen run
makes the one refactoring itself and lands it through the normal merge guard,
under the same evidence rules as a Maker. When the card emitter (item 5
below) exists, the prompt switches to filing cards and this section goes.

- **Trigger: schedule gated by master, not cards.** `line/kaizen.Runner`
  starts a run when `--interval` (default 24h) has passed since the last one
  *and* master differs from the master that run started on, and never while
  the andon cord is pulled. An unmoved master has no new hotspot, so Kaizen
  sleeps (no make-work). State is in memory; a restart runs once at once.
- **Signals measured.** Every run computes the guide's section 3 signals
  with the repo's own scripts (`line/kaizen/signals/`, python via `uv`, stdlib
  only, run in the station container), per Go package and for the repo:
  hotspot score (churn over 90 days of `git log` x cognitive complexity),
  hidden-coupling ratio (co-changing package pairs with no import edge),
  clone count, import cycles, dead symbols, pattern entropy (error creation
  and logging idioms) and the CI median runtime from the run store
  (`GET /v1/runs`). Complexity and the idiom counts are lexer-based
  approximations, comparable between runs of these scripts and not with
  other tools. The scripts are deterministic: the clock is the history
  revision's commit date, and a re-measure of the edited tree reads the same
  history (`--rev $BASE`), so before and after differ only by the edit. The
  structure analyzer (items 1-4) will replace this with line-computed
  findings.
- **Run order.** (1) `sweatshop line kaizen context` saves the CI runs and
  the previous run's signals; `kaizen_signals.py measure` writes
  `signals.json`. (2) `kaizen_signals.py report` renders `report.html` (a
  per-module table: signals, trend against the previous run, hotspot rank)
  and `sweatshop line kaizen publish` stores both under
  `reports/kaizen/<commit>/` in the object store, next to the CI reports
  (`POST /v1/kaizen/signals`), and records the baseline in
  the line database `<line>/line.db`, which `GET /v1/kaizen/signals/previous`
  serves back as the next run's trend. (3) The agent names its target from
  the ranked hotspots in the analysis report (category P1..P5, signal it will
  move). (4) After the change it re-measures, `kaizen_signals.py delta` prints
  the Signals line and exits 1 unless the signal dropped, and the report is
  published again with `signals-after.json` and the before/after table.
  A change whose signal did not move is not submitted.
- **Before/after evidence.** The change report from `line/guidance/kaizen.md`
  section 6 goes in the commit message body. `sweatshop line merge` refuses a
  Kaizen commit (`SWEATSHOP_KAIZEN` set) whose message lacks any report field
  or a `Signals: <signal>: <before> → <after>` line whose two values differ
  (`kaizen.CheckReport`).
  The guard itself is unchanged: it attests `just ci` for behavior, and the
  report stays in master's history for the delta-verification loop (item 8).
  The report is agent-authored, so it is a claim, not proof (P4) until the
  analyzer re-measures it.
- **Landing is detected like a Maker's.** The run ID `kaizen-<UTC time>` is
  the container's `SWEATSHOP_BEAD`, so the existing `Bead:` trailer check and
  `maker.Landed` apply unchanged.
- **Analysis report.** Before editing, the agent pipes a Markdown report of
  its measured signals and ranked findings to `sweatshop line kaizen report`
  (`POST /v1/kaizen/analysis`, kaizen role only). The line stores it in the
  same object store as witness reports (S3 when `SWEATSHOP_S3_BUCKET` is set,
  else `<line>/reports`) at `kaizen/<run id>/analysis.md`. A run that finds
  nothing worth changing still publishes its report.

## Open questions

- Which language adapters before Go? Is a polyglot graph worth it, or do we
  run one analyzer per ecosystem and combine scalars?
- Should the index be one scalar or a vector with per-component setpoints?
  A vector resists Goodhart but complicates heijunka.
- Does the non-regression check ever become default, and with what waiver
  mechanism?
- Should Makers executing Kaizen cards be a distinct lineage or role variant,
  so their track record separates structural from feature work?
- Speculative refactor in scratch (see [Station](#station-and-token-capabilities)):
  worth the spend?

## Proposed implementation cards

Ordered by dependency; each is one Maker change. Build-order placement is
step 6, after the spend ledger and heijunka (step 5).

1. **Structure analyzer core (Go).** Code graph from `go list`/`go/packages`:
   modules, symbols, import edges; per-module fan-in/out, cycles, size,
   API-surface ratio. CLI `sweatshop line analyze structure --json`. Tests on
   fixture module trees (cycles, shallow wrapper, hub).
2. **Per-commit trend store.** Persist analyzer results keyed by commit; query
   delta between two commits and over a window. Backfill from git history.
3. **Complexity index and setpoint.** Constitution `kaizen` table (weights,
   setpoints, window, card budget); index computation including build/test
   duration from the spend ledger; "no trend" semantics. `line init` writes
   defaults.
4. **Finding schema and hotspot ranking.** Closed finding schema, fingerprint
   dedup, churn-weighted hotspot ranking, low-confidence gating. Adversarial
   test: hostile strings in source never appear in finding text.
5. **Kaizen card emitter.** Card type with required metric-delta acceptance,
   evidence refs and goal family `kaizen`; station API route with per-window
   budget and dedup; rejects cards without a measurable criterion.
6. **Kaizen station runner.** `sweatshop line kaizen` and
   `line station add --role kaizen`: key, token, container launch; token
   scoped per the capability table (403 on attest/merge); prompt skeleton
   encoding mandate, card-only output and sleep-below-setpoint.
7. **Kaizen health metric into heijunka.** Index versus setpoint as the
   Kaizen metric feed; deficit and target share; sleep and wake rules.
8. **Delta verification loop.** After a Kaizen-card merge, re-measure on
   master, compare to the promised delta, close or reopen the card, and record
   realized delta in the track record.
9. **Test value analyzer, Kaizen half.** Redundancy, cost and flakiness
   findings; delete/merge candidate cards; routing of acceptance-path
   candidates to Gemba as requests.
10. **depmaint and bughunt adapters.** depmaint campaigns emit Kaizen cards;
    bughunt rule false-positive statistics feed Kaizen candidates.
11. **Dependency-closure export.** Expose the graph's reverse-dependency
    closure to the merge guard for scoped re-attestation (shared with the
    spec's deferred optimization).
12. **Optional non-regression check.** Analyzer-backed required check with
    tolerance and feature-card waiver; off by default.
13. **Track record and audit hooks.** Realized delta, churn caused, false
    hotspots, spend per index point; sampled re-audit. Depends on the agent
    runtime's track-record store (build order step 4).

Cards 1-6 are the minimum to see a Kaizen station file useful cards; 7-8
close the loop; 9-13 extend it.

## Running as a service

`sweatshop line kaizen` is the host-side loop (`line/kaizen`). It polls master
every minute and starts a run when `--interval` (default 24h) has passed since
the last run and master has moved.

On the server, with systemd (`line/deploy/systemd/sweatshop-kaizen.service`):

1. Install the `sweatshop` binary (`just install`) and a container engine the
   service can reach.
2. Put `SWEATSHOP_LINE_TOKEN=<kaizen station token>` and the model credential
   in the unit's `EnvironmentFile`.
3. Copy the unit to `~/.config/systemd/user/`, fix the paths, then
   `systemctl --user daemon-reload && systemctl --user enable --now sweatshop-kaizen`.
   `loginctl enable-linger $USER` keeps it running without a login session.

`Restart=always` restarts it on failure; a restarted runner runs once at once.

### Heartbeats and schedule

With `SWEATSHOP_LINE_URL` and `SWEATSHOP_LINE_TOKEN` set, the station
heartbeats every poll: `idle` between runs and `working <run id>` during one.
`sweatshop line status` lists it with `alive=` (heartbeat within three polls),
`last_run=` (start of the last run, absent before the first) and `next_run=`
(earliest time of the next; the time of the beat when it has never run). A run
also needs master to have moved, so `next_run` in the past with `idle` means
Kaizen is waiting for master.
