# Sweatshop assembly line — architecture

Status: approved design, pre-implementation.
Philosophy and vocabulary: [`assembly-line-principles.md`](assembly-line-principles.md).
This document defines entities, flows, contracts, and the build order. Each
component listed under [Components](#components) gets its own spec → plan →
implementation cycle.

## Entities and state

**Worker** — capacity: sandbox + compute + token budget. Fungible, stateless
between hostings. Holds no credentials.

**Agent** — persistent state, not a process:

- identity: stable ID, lineage (parent, generation)
- role: Maker | Andon | Kaizen | Gemba
- memory: evidenced lessons, inherited testament
- track record: attestations, merges, regressions caused, andon pulls (true and
  false), audit outcomes, spend
- in-flight work: claimed cards
- vitals: birth time, age (wall / spend / work units), lifetime limits

An agent's role never changes. A *worker* changes which agent it hosts.

**Hosting states:** `active` (loaded on a worker), `vacation` (dormant by
choice), `sleeping` (dormant flame-keeper, wakes on signal), `dying`, `dead`.

**Metamorphosis** — a worker unloads agent A and loads agent B, of any role.

**Lifecycle:**

- *Birth*: fresh (immigrant) or child of a parent (testament + variation).
- *Death triggers* (constitution): age limit, track-record floor, audit failure
  (immediate).
- *Dying*: finish or release claimed cards → write testament → `dead`. State is
  archived, never deleted (lineage, forensics).
- *Abrupt death* (worker or host crash): the evidence log survives in the
  witness, which runs outside the sandbox, and in its remote sync. Crashed
  containers are retained for post-mortem, then reaped. See
  [Testament after abrupt death](#testament-after-abrupt-death).
- *Testament*: every item cites evidence (invocation IDs, attestations, bead
  IDs); uncited items are dropped. Inherited lessons unconfirmed for N
  generations decay.
- *Variation*: LLM non-determinism gives free variation; the constitution may
  add larger jumps (model, prompt, context-mix variants).
- *Selection*: lineage track record decides whether a lineage may reproduce.
- *Flame*: each role keeps a minimum living population. A death that drops a role
  below its minimum triggers a birth — child of the best lineage, or an
  immigrant per the configured immigrant fraction.

**Session** — one hosting = one agentshd session, created with the agent's
role. The session role is immutable. agentshd holds the role's credentials and
acts and signs on the agent's behalf.

**Card** — unit of work in beads: goal family, evidence, acceptance criteria,
allowed concurrent claims (speculative K). Emitted by agents or by watchers.

**Line infrastructure** — deterministic, not agents: witness, guards,
constitution store, heijunka publisher, watchers, worker scheduler.

## Flow of a change

```
 card (beads) ──claim──▶ agent (session, role) ──work──▶ candidate tree
                                                            │
                         witness: run required checks ──────┤
                         signed attestation {tree, cmds,    │
                         image, exit codes, cfg version}    ▼
                                              merge guard: CAS on master
                                              ├ attestation valid + trusted key
                                              ├ tree == attested tree
                                              ├ checks ⊇ constitution's required set
                                              ├ line not stopped (in scope)
                                              └ ref still at expected base
                                                   │ base moved → rebase,
                                                   │ re-run checks, re-attest
                                                   ▼
                                     master ──▶ watchers: post-merge suites, deploy,
                                                telemetry, scans
                                                   │ signal
                                                   ▼
                                               cards / andon
```

1. **Pull.** The agent picks a card by its own judgment plus the heijunka
   signal. Claims are atomic (beads).
2. **Work.** In a sandbox, through an agentshd session. All commands are
   witnessed.
3. **Attest.** The agent asks the witness to attest a commit. The witness
   fetches it into a witness-owned repository, checks it out cleanly, runs the
   constitution's required checks there (in a container from the constitution's
   image; host-local only at trust level `none`), and signs an attestation over
   the exact tree hash. Checks never run in the agent's own container — the
   agent controls that environment and could fake a toolchain. The agent never
   holds the key and cannot forge an attestation.
4. **Merge.** The witness pushes on the agent's behalf; the merge guard verifies
   the attestation and performs the CAS. The serial section is the guard check
   plus the ref update.
5. **Lost race.** Base moved → rebase, re-run checks, re-attest. Deferred
   optimization: re-attest only when the targets that transitively depend on
   the candidate's changes intersect those that depend on the changes merged
   since its base; otherwise carry the attestation over and accept a window in
   which hidden dependencies (config, reflection, codegen, runtime coupling)
   are caught post-merge.
6. **Post-merge.** Watchers run slower suites, deploy (per-merge or batched,
   behind flags, per constitution), and collect field telemetry. Every deploy is
   change-tagged, so attribution is a query.
7. **Feedback.** Watchers emit cards. Evidence of failure on master or a deploy
   leads to an andon pull.

**Acceptance tests** live on paths the constitution classifies as acceptance;
changing them requires a Gemba session (role-gated). Makers satisfy tests and
cannot edit them.

**Speculative attempts:** a card may allow K concurrent claims; the first merge
wins, the rest are released and recorded.

## Andon and the circuit breaker

**Pull.** By default any agent may pull, with evidence: a signed attestation of
a failing check on master or a deploy, or a watcher signal signed by line
infrastructure. The andon guard verifies the evidence and records an **andon
event**: scope, evidence, puller, time, config version.

**Scope.**

- *Attributed*: change tagging names the culprit commit(s) → the stop is scoped
  to the culprit's dependency closure. Merges touching it are blocked; the rest
  of the line flows.
- *Unattributed*: global stop. The merge guard rejects all merges except fixes
  and reverts that reference the andon event.

**Swarm.** The event emits a top-priority card; any agent may respond, none is
ordered. The heijunka signal reflects the stop (integrity deficit spikes).
Agents that don't swarm keep working on branches; they just can't merge into
the stopped scope.

**Resolve.** Fix or revert at the responder's discretion. Resolution is an
attested passing run of the failing evidence's checks on the new master; the
andon guard then clears the event. Clearing is mechanical, never declared.

**Circuit breaker** — budget per event (attempts, wall time, spend):

1. Breach → line infrastructure auto-reverts to the last green commit in scope.
2. Revert not clean (e.g. an irreversible migration that slipped past the
   expand/contract guard) → freeze the scope and escalate.
3. Escalation → beads card to the constitution's escalation target (default:
   the king).
4. Budget burned on failed fixes is recorded in the responders' track records.

**Oscillation guard:** repeated andon events on the same scope within a window
→ global stop + escalate.

**False pulls:** evidence that does not reproduce under audit is recorded
against the puller's track record. Evidence plus consequence; no identity gate.

## Witness and trust

- One Ed25519 key per agentshd instance. Private keys never reachable from
  agent sandboxes.
- Attestations: in-toto statements in DSSE envelopes. Subject = tree hash;
  predicate = commands, environment image digest, exit codes, invocation IDs,
  session role, config version.
- Merged commits are signed by the witness (git SSH signing); the ref guard
  requires a trusted signer.
- Trust list = witness public keys in the constitution. Removing a key makes
  everything it signed suspect.
- Verification levels (constitution knob, CPU vs trust): attest-only →
  sampled clean-room audit at rate r → full re-run.
- **Deployment precondition:** agents run sandboxed in Linux containers (VMs
  later; no macOS sandbox); agentshd runs on the container host and only its
  socket is mounted in. On macOS the line runs inside a Linux VM.
  Unsandboxed operation is allowed only at trust level `none` (development).

Crypto is used only where trust crosses a boundary at rest (attestations,
commits). Agent → witness uses a local socket; witness → toolchains uses each
toolchain's own transport auth.

## Constitution

Versioned TOML, content-addressed, stored in a protected repo or path. Every
attestation, merge, andon event, and death records the config version it was
judged under.

Sections:

- **governance** — sovereign (default: king = owner); meta-rules listing
  parameters agents may self-amend and their bounds (default: none, i.e.
  absolute monarchy). The sovereign may amend the constitution into a
  constitutional monarchy or any other form.
- **trust** — witness public keys; verification level.
- **roles** — definitions, witness-held credentials, flame minimum, lifetime
  limits, death triggers, reproduction rules, immigrant fraction, variation
  knobs.
- **guards** — policy per guarded action: evidence-gated | role-gated | both.
  Defaults: andon evidence-gated; acceptance-test paths Gemba-gated;
  constitution changes sovereign-gated; merge evidence-gated.
- **checks** — required checks per path/target for merge; post-merge suites;
  rebase re-attest rule.
- **andon** — scope rules, breaker budgets, oscillation window, escalation
  target.
- **heijunka** — balancing plugin and parameters.
- **deploy** — per-merge | batched; flag requirement; expand/contract migration
  enforcement.
- **capacity** — pinned worker pools (reservations, not assignments); spend
  budget.

**Proposals.** Any agent may file a proposal card: a config diff plus evidence.
Inside meta-rule bounds, the config guard auto-applies it once the meta-rule's
evidence threshold is met. Outside, the sovereign decides. Every applied change
is a new version; versions are immutable.

**Bootstrap.** `sweatshop line init` writes the default constitution: king =
owner, defaults above, trust level `none` until witness keys are registered.

## Heijunka

**Plugin interface.** Input: spend ledger, goal health metrics, constitution
parameters. Output: the **heijunka signal** — per goal: target share, actual
share over a rolling window, deficit, card-priority multiplier. Deterministic,
recomputed on an interval, published to the master database. Any balancing model is
speculative, so the plugin is swappable.

**Default plugin:**

- *Spend ledger*: the witness records tokens, CPU-seconds, and wall time per
  session, tagged by role → goal family. Agents cannot misreport it.
- *Target shares*: constitution weights.
- *Health metrics* shift each goal's effective weight within constitution
  bounds:
  - Makers: card lead time, backlog age
  - Andon: open andon events, post-merge failure rate, time-to-green
  - Kaizen: complexity index (dependencies, volume, build/test time, coupling)
    vs setpoint
  - Gemba: field regression rate, SLO burn, acceptance coverage of reported
    issues
- *Damping*: rolling windows and bounded shifts prevent oscillation.

**Consumption** is advisory (soft rule). Agents read the signal when choosing
to pull, metamorph, vacation, or sleep. The worker scheduler uses it to wake
sleepers and to birth up to flame minimums. When no deficit is worth its spend,
agents vacation and workers are released; the rate of work may be zero while
the flame persists dormant.

## Components

| Component | Owns | Built on |
|---|---|---|
| Witness | sessions, role binding, held credentials, attestations, commit signing, spend ledger | agentshd |
| Guards | merge CAS, andon, config, role-gated paths | git server hooks / forge rules, beads atomic claims, `sweatshop-guard` |
| Constitution | schema, versions, proposals | TOML in git |
| Agent runtime | agent state, lineage, lifecycle, worker scheduler | master DB, object store, Linux containers via pluggable scheduler |
| Heijunka | signal | plugin over spend ledger + metrics |
| Watchers | post-merge suites, telemetry, scans → cards | deterministic processes |
| Stations | role prompts, skills, tool sets | Claude Code, pi.dev |
| Audit (v2) | clean-room re-runs, key revocation | witness + workers |

### Existing tools, repositioned

- **beads** — the kanban: cards, claims, escalations.
- **agentsh** — witness foundation. Required changes: session ↔ role binding;
  witness-held credentials and acting on the agent's behalf (push, beads
  writes); Ed25519 keys and DSSE/in-toto attestations; git commit signing;
  spend ledger; split deployment (daemon outside sandbox, socket only inside).
- **bughunt** — Andon/Kaizen tool and watcher. Required changes: drop the
  "PR only with consent" path (fixes go through the line); a deterministic scan
  mode usable as a required check under attestation; findings emitted as cards;
  rule false-positive rates feeding Kaizen (rules are code too).
- **depmaint** — Kaizen tool. Required changes: campaigns become card
  generators; any agent or watcher may propose a campaign (the king approves
  only if the constitution says so); attempts verified via attestation.

### New tools

- **Structure analyzer** (Kaizen). Infers cohesion, coupling, and modularity
  from the code graph; measures volume as API surface vs internal complexity
  per module (deep vs shallow modules); tracks trends per commit. Feeds the
  Kaizen health metric and the complexity setpoint, emits cards for hotspots,
  and provides the dependency closure the merge guard uses for re-attestation.
- **Field analyzer** (Gemba). Ingests production telemetry and usage: errors
  and latency attributed via change tags, regression detection against
  baselines, usage per feature/endpoint/flag (unused features → Kaizen removal
  cards; heavily used paths → test-value weight). Reads untrusted input, so it
  emits structured findings only and holds no write capability beyond cards.
- **Test value analyzer** (Kaizen + Gemba; tests are code). Judges each test
  by: fault-detection power (mutation score contribution), unique coverage vs
  redundancy with other tests, cost (runtime, flakiness — muda/muri), and
  linkage to field reality (does it cover paths users exercise and failures the
  field has seen). Outputs: delete/merge candidates (Kaizen), missing-meaning
  gaps where field failures lack tests (Gemba), and slow/flaky suites (muri).

## First slice

Dogfood on the sweatshop repository.

- Constitution v0: king; required checks = `just ci`; trust level attest-only.
- Witness v1: session role binding; signed attestation over the tree for
  `just ci`.
- Merge guard v1: verify attestation + CAS on master, against a local bare repo
  as the remote.
- One Maker and one Andon, sandboxed, pulling from beads. Andon is global-stop
  only.
- Out of the slice: heijunka, lifetimes and lineage, Kaizen and Gemba stations,
  audit, scoped andon, the new analysis tools.

## Testing

- **Witness and guards:** table-driven unit tests plus adversarial cases —
  forged attestation, tree mismatch, stale base, unknown key, stopped line,
  role-gated path touched by the wrong role.
- **Slice end-to-end:** a scenario harness driving scripted fake agents (no
  LLM): claim → attest → merge → break master → andon → fix → clear, plus a
  lost CAS race (rebase, re-attest, retry). LLM agents join
  only once the harness is green.

## Build order

1. Witness v1 (agentsh changes) and merge guard v1, with the scenario harness.
2. Constitution v0 and `sweatshop line init`.
3. Sandboxed Maker + Andon stations; andon guard (global stop); first live
   dogfood.
4. Agent runtime: state store, hosting, metamorphosis, vacation/sleep, flame.
5. Spend ledger + heijunka default plugin.
6. Structure analyzer → Kaizen station; depmaint and bughunt repositioned.
7. Field analyzer and test value analyzer → Gemba station.
8. Lifetimes, testaments, lineage, selection.
9. Scoped andon, circuit breaker auto-revert, oscillation guard.
10. Sampled audit and key revocation; proposals with meta-rules.

## Runtime and storage

**Workers** run as Linux containers placed by a pluggable scheduler: local
container runtime in dev, Nomad / Kubernetes / ECS when shared. VMs are a later
sandbox option; the macOS sandbox is out. agentshd runs per container host,
outside the sandbox.

**Storage** is split by data shape:

| Data | Store |
|---|---|
| Agent state, hosting leases, track record, spend ledger, heijunka signal, andon events | One master database behind a storage interface. v1 backend: Turso (embedded file in dev; `sqld` or Turso Cloud when shared). Expected to suffice long-term; Postgres only if Turso is outgrown. |
| Invocation blobs, logs, reports, attestations, container remains | S3-compatible object store (S3, MinIO, R2), content-addressed. Local filesystem in dev. |
| Cards, claims, escalations | beads |
| Constitution, code | git |

Single master, not multi-master: hosting leases, claims, and andon/merge state
need linearizable compare-and-swap, which multi-master replication cannot give
(conflicting revisions would let two workers host the same agent). Writers are
few — agents never write directly; witnesses and line infrastructure do.

## Testament after abrupt death

A testament has two parts:

- **Mechanical** — derived deterministically from evidence: cards claimed,
  invocations, failures, attestations. Always produced.
- **Distilled** — lessons. Written by the agent on orderly death, or by an
  *executor* (a separate session) after abrupt death, from evidence and
  remains. Citation rule applies to both; the executor cannot invent.

| Failure | What survives | Testament |
|---|---|---|
| Worker/container crash | witness log; container filesystem (retained) | mechanical + executor reads remains (working tree, scratch, partial diff) |
| Host crash | witness remote sync; object store | mechanical from synced evidence |
| Total loss | beads claims, merged commits | gap marker in lineage; child born as immigrant |

## Deferred

- **Dependency-closure re-attestation.** Optimization of the lost-race path;
  v1 always re-attests. Needs per-language reverse-dependency graphs (`go list`,
  Bazel `rdeps`, Python import graph), later supplied by the structure analyzer.
- **Multi-master storage** (e.g. CouchDB) — only if partitioned multi-site lines
  become real; requires a separate linearizable lease service.
