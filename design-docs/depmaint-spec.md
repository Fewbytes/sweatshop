# depmaint — dependency maintenance

Status: design. Date: 2026-09-26.

## Purpose

Keep a repository's dependency graph — packages and runtimes — moving: upgrades
made feasible, chains short, nothing deprecated, EOL, or vulnerable sitting
still. The tool expands the full transitive dependency graph, caches it together
with external metadata (versions, advisories, EOL dates, licenses), and drives
bounded **campaigns** that analyze blockers, issue work orders for agents,
record verdicts, and iterate until the goal is reached, the budget is spent, or
the options are exhausted.

Humans commission campaigns and read reports. Agents execute attempts. The
databases make verdicts compound across the team: a blocker analyzed once is
never re-analyzed, an attempt rejected once with a reason is never re-attempted.
That is the tool's whole claim — every campaign leaves a shorter path for the
next one.

## Position in sweatshop

- **agentsh** — attempts run through it, so raw agent commands and output are
  retained as replayable blobs and attempt rows carry invocation ids. Soft
  dependency: absent daemon → attempts still recorded, evidence refs null.
- **beads** — the human-decision queue. Campaigns live in depmaint; a campaign
  stalled on a human decision (policy, risk acceptance, more budget) escalates
  to beads and carries the bead id. Beads never sees graph rows or attempt
  detail, only the escalation with its report attached.
- **bughunt** — the gate. An attempt's verification includes `bughunt scan`;
  new findings block a `succeeded` verdict. Same tool family, same sync pattern,
  separate databases.
- **depmaint** — own binary, own Dolt database for campaign state, own
  disposable SQLite cache for graph and enrichment.

Built for teams, not solo developers: shared verdicts and shared candidate
rationale are the point. Everything that is merely cache is local and
disposable; everything that is a decision syncs.

## Non-goals

- Not a Renovate clone. No standing auto-PR service; campaigns are ordered,
  bounded work. (Renovate's data model is worth studying; its always-on
  bot posture is not what agents need.)
- Not a lockfile manager or resolver. Expanders read what the ecosystem's own
  tools already resolved; depmaint never rewrites manifests itself — agents do,
  inside attempts.
- Not an SBOM/compliance suite. Licenses and advisories are cached and joined
  into candidate scoring and audit; policy engines are out of scope for v1.
- No LLM inside the binary. depmaint is deterministic where it computes
  (graph, blockers, scores, plan) and *produces the prompts agents execute*
  (work orders, below); it records what agents report. The loop is driven from
  outside via `next`/`record`, so an interactive agent, a headless CI runner,
  or a human can all drive the same campaign interchangeably.

## Concepts

### Graph snapshot

An expansion of the transitive graph from the repo's manifests and lockfiles:
nodes (`package@version`, or `runtime@version`) and edges (requirement →
resolved). Snapshots are content-addressed and immutable — each expansion
writes a whole new snapshot and points the root at it; unchanged tree → no-op.
Diffing snapshots is how drift, upgrade effect, and removal wins are measured;
`graph` reports the delta against the previous snapshot by default.

**Resolved vs published truth.** Expander adapters produce *resolved* truth
from lockfiles (what is). deps.dev supplies *published* truth (what could be:
latest versions, requirement ranges, licenses, advisories, deprecation flags).
Both are cached, keyed separately — feasibility analysis needs published,
blocker analysis needs resolved, and the two disagree exactly where the
interesting work is.

### Enrichment cache

Advisories (OSV, via deps.dev batch queries), EOL dates (endoflife.date),
latest-version and deprecation flags (deps.dev), licenses. Per-source TTLs
(advisories short, versions medium, EOL long). Disposable: loss = one refetch.
Refresh is stale-while-revalidate: serve the cached copy immediately,
refresh in the background; on refresh failure the stale copy is served with
its age noted. Offline operation reads stale cache and says so, never fails.

### Campaign

A bounded unit of work: type, target, goal, budget, status. One campaign, one
goal — bundles are reports that group campaigns, not a super-campaign type.
**Projects, not roots.** A project is defined by the graph's *internal*
edges: roots connected through dependencies that live inside the repo —
workspace members, path/`file:` deps — are one project. Two graphs that
merely import the same package from the internet are **not** merged:
coincidental external overlap creates no coordination need. Roots are
expansion units (snapshots stay per-root, content-addressed); projects are
planning units: the planner unions the per-root graphs connected by internal
edges at plan time, so cross-root coordination inside a project is free, and
across projects there is nothing to coordinate by definition. A monorepo is
typically several projects. Campaigns pin their root set at creation (stable
identity); the project graph is recomputed live at each plan.

Types:

1. **upgrade** — move package P to target version (or a set S). The planner
   computes the **blocker set**: every node whose requirement constraints
   cannot accept the target, directly or transitively. It then searches for the
   coordinated upgrade set — which concurrent dep upgrades make the campaign
   feasible — tier-ordered after Renovate's grouping semantics: patch-only
   sets first, then minor-coordinated sets, majors last; within a tier,
   minimal sets first, then sets that unblock other open campaigns. Cheap,
   reversible moves are tried before expensive ones. Output is a plan: ordered steps with per-step rationale,
   expected verification, and rollback; each step is issued to an agent as a
   work order.
2. **runtime** — move a runtime (Go toolchain, Node, Python, JVM) to target.
   Modeled as an upgrade whose root node is the runtime: the same planner runs,
   but blocker kinds differ — `go` directives, `engines` fields,
   `requires-python`, toolchain pins, and (advisory, not gating) base images and
   CI matrix files. Shared feasibility core; distinct blocker semantics. A
   package edge that constrains the runtime (an old dep requiring Python <3.12)
   is just an edge the upgrade planner already understands.
3. **remove** — eliminate a direct dependency. Candidates are scored:
   transitive subtree size introduced, usage-subset ratio (cheap prior: import
   counts vs. package size; refined by agent exploration), alternative already
   present in the graph, health flags (deprecated/EOL/advisories), staleness.
   Exploration is agent work with a fixed option set: delete (unused), swap to
   an in-graph alternative, reimplement the used subset, vendor the used
   subset. The exploration *findings* — what the subset is, how central it is
   to our behavior (semantic importance — judgment, not counting), what the
   swap costs — are cached on the candidate, because that judgment is
   expensive and reusable.

**Hygiene is a driver, not a type.** `audit` joins enrichment against the
snapshot: deprecated, EOL, and vulnerable deps become a prioritized worklist of
upgrade/remove campaign suggestions. Ordering campaigns from that worklist is
the hygiene loop; severity floors are config.

### Work order

The agent-facing artifact: a self-contained prompt for one attempt. One work
order per planned step (upgrade, removal, fix) or exploration task, written
to `.depmaint/work/<campaign>-<n>.md` and committed — a team must be able to
review in a diff exactly what agents were asked to do, and an agent must be
able to execute from the file alone, with no depmaint knowledge beyond the
report-back command printed at the end.

Sections, always:

1. **Goal and step** — campaign, step n of m, target movement, attempt budget.
2. **Why** — the blocker analysis or candidate rationale that produced this
   step, verbatim.
3. **The change** — manifests to edit and the expected version movements;
   published break signals where known (changelogs, major-version flags).
4. **Relevant subgraph only** — the nodes and edges this step touches, never
   the full dump. Context is bounded by design.
5. **Prior attempts** — earlier verdicts on this campaign with reasons, so the
   executor does not repeat a rejected approach. This is where shared verdicts
   pay for themselves a second time.
6. **Verification** — build, tests, `bughunt scan` clean, and the *expected
   snapshot delta* (nodes/edges that should appear or disappear); `record`
   diffs actual against expected.
7. **Report-back** — the literal `campaign record` command with placeholders.

Generation is deterministic: same campaign, plan, and snapshot →
byte-identical work order. Golden files test it.

### Attempt

An execution of a work order by an agent. The work order is the deterministic
spec; attempts are minted at `record` time and labeled by executor, so several
agents can execute the same work order concurrently, each producing an
independent row: work order ref, executor, verdict, reason, PR/commit refs,
snapshot before/after, bughunt scan result, cost. Verdicts are append-only,
and their semantics drive the loop:

- `succeeded` — step landed, verification green. depmaint re-snapshots, diffs
  actual vs. expected delta, and **re-plans**: the coordinated upgrade set was
  a hypothesis; reality may have cleared or added blockers.
- `rejected` — reason required (code-side judgment: API break too large,
  semantics changed, migration too invasive). Feeds planning: split the step,
  choose the alternative upgrade set, propose a removal instead, or mark the
  blocker `waived` — which is a human decision, i.e. escalation.
- `failed` — environmental (network, toolchain, flaky build). No judgment
  recorded; re-dispatch allowed; counts against budget.
- `findings` — exploration verdict (remove campaigns): a structured doc —
  used subset, how central it is to our behavior, alternatives considered,
  effort estimates — cached on the
  candidate row. Exploration is the expensive judgment; it is done once and
  reused by every later campaign against that package.
- `superseded` — a verdict arriving after the step already completed at a
  newer snapshot epoch (a concurrent executor won the race): the attempt row
  stands as history, but it neither re-plans nor gates. Redundant successes
  and stale-snapshot rejections land here.

**Optimistic parallel dispatch.** LLM executors are not deterministic, so
racing a step across several agents is a legitimate strategy — one verified
success completes it. Fan-out needs no new protocol: tell N agents to run
`campaign next` (or wire a CI job with N runners); all receive the same work
order, and `next` is idempotent while a step is in flight. When one
`succeeded` lands and re-plans, the others' verdicts arrive as `superseded` —
and a superseded rejection can never suppress re-dispatch, because
suppression is keyed on (step, snapshot, plan) and the winning re-plan
changed the snapshot. A racer that failed needs no semantics at all:
failures never gate. Every attempt counts against budget — racing is
deliberately spending budget for redundancy, and the accounting shows it.
When several racers open PRs, the attempt log shows all of them; PR dedup is
the campaign owner's call, not the tool's.

**Rejection suppression.** A work order is never re-emitted with identical
(step, snapshot, plan). Re-dispatch requires a changed graph or a revised
plan. Verdicts live in Dolt, so suppression holds across the team — an
approach rejected by one agent on one machine is not re-attempted by another.

### Report

Markdown, human-first: goal, status, budget bar, graph delta since campaign
start, current blockers and why, the open work order, options considered and
rejected with reasons, attempt log. Reports are files (default
`.depmaint/reports/<campaign>.md`), committed — reviewable in a diff, durable
for the humans ordering the campaign. A CI scheduled task refreshes the graph,
renders open campaigns' reports, and (opt-in) drives the next/record loop
headless. The report is the escalation artifact too: a beads escalation
references it rather than restating it.

## Commands

```console
depmaint init                        # create .depmaint/, Dolt db, config
depmaint graph [--refresh]           # expand + snapshot; report drift vs previous
depmaint why <pkg>[@ver]             # reverse-dependency chains, all paths
depmaint outdated [--ecosystem ...]  # direct deps with newer published versions
depmaint audit [--min-severity ...]  # advisories + EOL + deprecated join → worklist
depmaint candidates [--type remove]  # scored removal candidates with rationale

depmaint campaign new --type upgrade|runtime|remove --target ... [--budget N]
depmaint campaign plan <id>          # (re)compute blockers + plan; deterministic
depmaint campaign next <id>          # emit the next work order (summary + file path)
depmaint campaign record <id> --step <n> --verdict ...       # executor reports back
                                     #   [--reason] [--findings <path>] [--pr <url>]
                                     #   [--executor <name>]
depmaint campaign report <id> [--out <file>]
depmaint campaign list [--status ...]
depmaint escalate <id> --reason ...  # → beads

depmaint sync                        # dolt push/pull campaign db
depmaint stats
```

`plan` / `next` / `record` are deliberately the smallest agent loop: the tool
holds state and analysis; any driver (interactive agent via the skill, CI
runner, human) alternates `next` and `record` until `next` returns
`succeeded` / `exhausted` / `blocked`.

Budget defaults per campaign: attempts and wall-clock, whichever first.
Exhausted → status `exhausted`, report rendered, optional beads escalation.

**Team posture vs campaign parameters.** `.depmaint/config.yaml` carries
team posture — severity floors for `audit`, ignored packages and ecosystems
— committed and reviewable. Campaign `new` carries instance parameters
(target, budget) and may override posture for that campaign. Posture living
only in campaign creation means every campaign re-encodes it and they drift.

## Workflow

The tool drives; agents execute. Every state embeds the literal next call
(the agentsh §5 pattern), so a driver needs no protocol knowledge — an agent
told *"run depmaint and follow its output"* is a complete driver.

**Execution loop** (upgrade, runtime, and remove campaigns alike):

```console
$ depmaint campaign next dep_c1
campaign dep_c1 — upgrade go.uber.org/zap 1.24 → 1.27 (bughunt/go.mod)
step 2/3 · work order: .depmaint/work/dep_c1-002.md
→ read .depmaint/work/dep_c1-002.md, execute it, then:
  depmaint campaign record dep_c1 --step 2 --verdict <succeeded|rejected|failed> \
    --reason "..." [--pr <url>]
```

**Exploration loop** (remove campaigns, until findings are cached):

```console
$ depmaint campaign next dep_c7
campaign dep_c7 — remove github.com/pkg/errors (bughunt/go.mod)
exploration · work order: .depmaint/work/dep_c7-001.md
→ read .depmaint/work/dep_c7-001.md, investigate, then:
  depmaint campaign record dep_c7 --step 1 --verdict findings --findings <path>
```

Once findings land, `plan` picks the strategy (delete, swap, reimplement,
vendor) and the campaign becomes an execution loop.

**Two wirings, same loop.** Interactive: a user tells an agent to run
depmaint and follow its output — the skill encodes the sequence. Wired: a CI
scheduled task runs `next`, dispatches the work order to a headless agent
runner, records the verdict back. There is no protocol difference between
the two; the driver is interchangeable, which is what keeps both first-class
instead of one being a bolt-on. Either wiring may fan out — dispatch the same
`next` to several executors — since attempts are append-only and one verified
success completes a step (see Optimistic parallel dispatch).

## Adapters and sources

Expander contract (bughunt's detector shape):

- `available() bool` — is the ecosystem's toolchain installed?
- `expand(root) → nodes, edges` — resolved truth, from lockfiles via the
  ecosystem's own tools (`go mod graph` / `go list -m all`; `npm ls --json`,
  pnpm/yarn equivalents; pip/pipenv/poetry/uv; `mvn dependency:tree`,
  `gradle dependencies`). Edges carry an `internal` flag — repo-local
  dependencies (workspace members, path/`file:` deps) are marked, since
  project derivation keys on it.
- `manifests(root) → direct deps, runtime constraints` — declared truth, needed
  for blocker and removal analysis.

v1 ecosystems: Go, Node, Python, JVM. Detection by manifest presence; missing
toolchain → warn, skip, record `expander_unavailable` on the run — never fail
(bughunt's rule: a gate that fails because a linter isn't installed gets
disabled by the first person it annoys).

Enrichment sources: deps.dev (versions, requirement ranges, licenses,
advisories — it fronts OSV; batch queries, no auth), endoflife.date (runtime
EOL). All cached with per-source TTL.

## Storage

Two stores, split by cost of loss:

1. **Cache — local SQLite** (`.depmaint/cache.db`, pure Go). Snapshots, nodes,
   edges, enrichment. Never synced. Deleting it costs one re-expand + refetch;
   snapshots prune to the last N per root. One Dolt db for this would put
   regenerable data on the sync path and bloat the shared history — rejected.
2. **Campaign state — Dolt** (`.depmaint/dolt/`), synced over the git remote
   like beads, but under depmaint's own ref namespace (`refs/dolt/depmaint/data`
   and friends) — beads owns `refs/dolt/data` and two tools must never write
   one ref. Cell-level merge is the point: two agents recording attempts
   concurrently on different machines must not lose a verdict. A passive JSONL
   export of campaign tables rides along for grep/diff, same as beads.

### Cache tables

- `snapshot` — id, root, hash, created_at, expander, node/edge counts
- `node` — snapshot_id, key (`ecosystem:name@version`), kind
  (`package` | `runtime`), direct
- `edge` — snapshot_id, from, to, requirement, resolved, internal (repo-local)
- `enrichment` — source, key, payload (JSON), fetched_at

### Campaign tables

- `campaign` — id, type, target, roots (pinned at creation), goal, status (`planned` | `running` |
  `blocked` | `succeeded` | `exhausted` | `abandoned`), budget, spent,
  created_at, updated_at, report_path
- `attempt` — id, campaign_id, step, work_order_ref, verdict, reason, executor,
  pr_url, commit_sha, snapshot_before, snapshot_after, bughunt_run, cost,
  created_at
- `blocker` — campaign_id, node, kind (`constraint` | `toolchain` | `engine` |
  `ci` | `base-image`), detail, status (`open` | `cleared` | `waived`),
  cleared_by (attempt)
- `candidate` — id, package, score, rationale (JSON — including agent
  exploration findings), status (`proposed` | `exploring` | `campaign` |
  `rejected` | `removed`)
- `escalation` — id, campaign_id, reason, bead_id

## Integration

- **agentsh discovery** — probe the socket; present → attempts carry
  invocation ids; absent → direct execution, null refs.
- **beads escalation** — probe `bd` + `.beads/`; absent → `escalate` reports
  unavailability, never fails the campaign.
- **bughunt gating** — after each attempt, run `bughunt scan`; new findings
  downgrade `succeeded` to `failed` with the scan run recorded.
- **Skill** — `skills/depmaint/SKILL.md` encodes the interactive wiring: run
  `campaign next`, read the work order, execute it, run `campaign record`,
  repeat until terminal. Without the skill the loop is improvised and
  forgotten under context pressure (`design-docs/ideas.md` constraint); with
  it, "run depmaint and follow its output" is a complete instruction.

## Testing

- Fixture repos per ecosystem with planted versions and constraint conflicts;
  assert expansion, blocker computation, and coordinated-upgrade-set search
  against hand-computed graphs.
- Determinism: same snapshot → byte-identical plan and work orders (golden
  files).
- Candidate reuse: exploration findings cached once are served to later
  campaigns against the same package without re-exploration.
- State machine: budget exhaustion, rejection suppression (identical work
  orders never re-dispatched), waiver semantics, concurrent attempt recording
  across two Dolt clones.
- Dogfood: sweatshop itself — a multi-project monorepo (Go project in
  `bughunt/`, Node project in `packages/`) — from the first working `graph`;
  project derivation via internal edges is exercised on day one.

## Open questions

Resolved during review and folded into the body: advisory refresh is
stale-while-revalidate (Enrichment cache); campaign target is a project —
defined by repo-local dependency edges (internal sharing merges, coincidental
external overlap does not), roots pinned at creation, planner unions
internally-connected graphs (Campaign); `ci` and
`base-image` blockers stay advisory — a gating parse bug is worse than an
embarrassing report (Runtime type); team posture lives in
`.depmaint/config.yaml` with per-campaign override (Commands); Renovate's
grouping semantics are the planner's search ordering — patch sets, then
minor-coordinated, then majors (Upgrade type); optimistic parallel dispatch
is first-class, first verified success wins (Attempt); import counts are the
cheap usage tier — semantic importance is exploration's judgment, delegated
to agents, not counting (Remove type).

Still open:

- deps.dev behavior on very large graphs (batch ceilings, rate limits) —
  empirical, resolved during implementation with backoff. The cache design
  assumes it is a tuning problem, not an architecture problem.
- Superseded semantics: v1 never replaces a winning attempt with a later
  verified success. Verification is identical for all racers (tests, scan,
  expected delta), so a later win is not a better outcome by the tool's own
criteria — revisit only if dogfooding proves otherwise.
  Decision (2026-10-18, sweatshop-qh0): deferred, v1 stands. No dogfood
  campaign has recorded a superseded success yet, so there is no evidence.
  Trigger to reopen: a campaign where a superseded success was measurably
  better than the winner (smaller diff, wider verified delta, or the winner
  later reverted) and the difference was not captured by the verification
  criteria themselves.
