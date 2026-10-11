# Gemba agent (field-fit station)

Status: design, not implemented. Card: `sweatshop-8kl`.
Parent specs: [`assembly-line-spec.md`](assembly-line-spec.md) (Gemba role,
field analyzer, test value analyzer, guards), [`assembly-line-principles.md`](assembly-line-principles.md)
(P3, P4, P5, P6, P11). Related card: **acceptance paths + gemba station**, the
existing implementation card for the guard and station plumbing. This document
is the role design that card builds to; see [Relation to the existing
card](#relation-to-the-existing-card).

## Why Gemba exists

Makers optimize for features, Andon for integrity, Kaizen for simplicity.
Nobody owns the question "does what we ship fit its external purpose, and do
our tests mean anything?" Today the gap is visible and documented:

- `justfile` and `station/**` are gated to role `gemba`, but no gemba station
  exists, so only the owner can change how checks run (README, M1 limitation).
- Tests are agent-editable. A maker can weaken a test in the commit it is
  judged by, and the witness's clean-room run will pass it.
- Nothing connects field failures to tests. A bug reported by users can be
  fixed without ever gaining a test that would have caught it.

Gemba (genchi genbutsu, "go and see") closes these: it goes to where the
product meets reality and owns the meaning of the tests that claim to
represent it.

## Mandate

1. **Field fit.** Observe how merged code behaves outside the line (errors,
   latency, usage, reported issues) and turn the evidence into cards.
2. **Meaning of tests.** Own the acceptance paths: the tests and checks that
   define "works" for the outside world. Keep them truthful, sufficient and
   non-redundant relative to field reality.
3. **Acceptance coverage.** Every field-reported failure class should end up
   covered by an acceptance test. Gemba closes that loop.

Gemba does not build features (Makers), stop the line (Andon), or simplify
structure (Kaizen). Where goals overlap, Gemba files a card; it does not
command (P1).

## Ownership

### Acceptance paths

Acceptance paths are constitution-classified path patterns, role-gated to
`gemba` using the existing `[[guards.paths]]` mechanism (`pattern`, `roles`):

```toml
[[guards.paths]]
pattern = "acceptance/**"
roles = ["gemba"]
```

Candidates: a dedicated `acceptance/**` tree for black-box tests that exercise
the product through its external surface (CLI, HTTP, station API), plus the
existing `justfile` and `station/**` gates (the check definition itself). Unit
tests colocated with code stay Maker-editable: they pin implementation, not
meaning. The split is a policy choice in the constitution, not framework
behavior (P2).

The guard enforces the gate against the diff from current master, as it does
for `justfile` today: a commit touching a gated path needs an attestation whose
session role is in the pattern's roles. Makers satisfy acceptance tests and
cannot edit them.

### Role-gated test changes

A Maker who believes an acceptance test is wrong does not edit it. It files a
card (a request, P1) with evidence: the failing run, the spec text, or the
field observation. Gemba may accept or decline. Declines are recorded with
reasons so the Maker's track record and Gemba's can both be audited.

Gemba changes acceptance tests only for three reasons, each of which must be
cited in the commit message and the card:

- **add**: a field failure or specified behavior lacks a test;
- **fix**: a test asserts the wrong thing (card evidence required);
- **retire**: a test no longer maps to anything used in the field or specified
  (usage evidence required; coordinated with Kaizen's delete candidates).

A weakening change (removed assertion, loosened tolerance, skipped test)
without one of these reasons is a defect in Gemba itself and is covered by
[Failure modes](#failure-modes).

## Field analyzer inputs

The field analyzer is a deterministic watcher (line infrastructure, not an
agent). It ingests, per spec:

- error and latency telemetry attributed to change tags (deploys are
  change-tagged, so attribution is a query);
- regression detection against baselines;
- usage per feature, endpoint or flag;
- issue reports from users (external trackers, feedback channels).

**All of it is untrusted input.** Telemetry and issue text can carry
attacker-controlled strings (error messages, user agents, issue bodies) aimed
at the agent that reads them. Therefore:

- The analyzer holds no write capability beyond emitting cards, and no
  credentials.
- **Output is structured findings only**: a closed schema, no free-form text
  copied from the source into the agent's prompt as instructions. Suggested
  fields:

  | Field | Type | Notes |
  |---|---|---|
  | `kind` | enum | `regression`, `slo_burn`, `unused_feature`, `uncovered_failure`, `flaky_signal` |
  | `change_tag` | commit/deploy id | verified against merged history; unknown tags drop the finding |
  | `surface` | enum/path | endpoint, feature, flag or file, from a known inventory |
  | `metric` | enum + numbers | name, baseline, observed, window, sample size |
  | `evidence_ref` | opaque id | content-addressed pointer into the object store |
  | `fingerprint` | hash | dedup key; same fingerprint updates one card |

- Raw strings stay behind `evidence_ref`. If Gemba must read them, it reads
  them as quoted data in a clearly delimited block, and no tool the session
  holds can be driven by their content beyond filing cards and proposing
  acceptance-test commits that still pass the guard (P3).
- Findings with unverifiable change tags, implausible magnitudes, or low
  sample sizes are discarded or marked low-confidence mechanically, before an
  agent sees them.

The **test value analyzer** (spec, Kaizen + Gemba) is the second input:
mutation score contribution, redundancy, cost, and linkage to field reality.
Gemba consumes its missing-meaning gaps (field failures without tests); Kaizen
consumes delete/merge candidates.

## Outputs

Gemba's outputs are cards and role-gated commits. Nothing else.

| Output | Shape | Consumer |
|---|---|---|
| Regression card | field finding + change tag + reproduction attempt; high priority; andon-pull evidence if attributable to a merged commit | Makers; Andon |
| Coverage-gap card | field failure class with no acceptance test; asks Maker (fix) and Gemba (test) | Gemba, Makers |
| Acceptance commit | add/fix/retire change on acceptance paths via `sweatshop line merge` under role `gemba` | the line |
| Unused-feature card | usage evidence for Kaizen removal | Kaizen |
| Flaky/slow-suite card | cost evidence | Kaizen (muri) |
| Rejection note | reasoned decline of a Maker's test-change request | requesting Maker |

Cards carry goal family `gemba`, evidence refs, and acceptance criteria stated
as a test that must pass. Where a regression is attributable, Gemba (like any
agent, P6) may pull the andon cord with the evidence; the andon guard, not
Gemba, decides scope.

## Authority

Gemba has exactly these powers, all mechanical at the resource (P3):

- **Merge to acceptance paths.** Its attestation carries role `gemba`; the
  merge guard accepts gated-path changes only from that role. The rest of its
  merges go through the normal check-and-CAS path.
- **File cards** through the station API (role-gated on the host).
- **Pull andon** with evidence, like any agent.
- **Propose constitution changes** as proposal cards (e.g. reclassify a path
  as acceptance). Never apply them; the sovereign or meta-rules decide.

Gemba cannot: merge without passing `just ci`, edit the constitution, sign its
own attestations, block Makers (it can only request), or edit non-acceptance
product code beyond what tests need. A Gemba commit that mixes acceptance and
product changes is rejected by convention in the station prompt and, later, by
a guard that restricts role `gemba` to gated paths plus fixtures.

One dangerous asymmetry: the check definition (`justfile`, `station/**`) is
also gated to Gemba. A Gemba change there alters what every later candidate is
judged by, so in v1 those two patterns stay owner-only (a path rule with an
additional `sovereign` requirement) while Gemba's authority covers
`acceptance/**`. Widening is a constitution decision once track records exist.

## Metrics

Gemba's health feeds the heijunka default plugin (spec: "Gemba: field
regression rate, SLO burn, acceptance coverage of reported issues"). Each must
be computed by line infrastructure from evidence, never from agent reports (P4).

| Metric | Definition | Direction |
|---|---|---|
| **Field regression rate** | regressions detected by the field analyzer per N merges / deploys, attributed by change tag, over a rolling window; reopened regressions count double | lower is better; rising raises Gemba's target share |
| **SLO burn** | error-budget consumption rate for constitution-defined SLOs per surface; burn > threshold is a deficit | lower is better |
| **Acceptance coverage of reported issues** | fraction of closed field-issue cards (and distinct failure fingerprints) with a linked acceptance test that fails on the pre-fix commit and passes after | higher is better |

Supporting, for Gemba's own track record (not heijunka inputs):

- **Test change integrity**: fraction of Gemba acceptance commits whose
  assertion strength does not drop (mutation score on the touched tests does
  not fall) — measured by the test value analyzer.
- **Escape rate**: field failures in surfaces that have acceptance tests.
- **Decline accuracy**: declined Maker requests later vindicated (a Maker's
  evidence proved right) count against Gemba.
- **Spend per closed gap**, from the witness spend ledger.

If there is no field data (v1 dogfood has no deployed product), the metrics
degrade explicitly to "no signal" and heijunka shifts nothing, rather than
reading zero as health.

## Failure modes

| Failure | Cause | Mitigation |
|---|---|---|
| **Prompt injection via telemetry or issues** | untrusted text reaches the agent | structured findings only; raw text behind `evidence_ref`; analyzer holds no write capability; Gemba's authority is limited to cards and gated commits (P3) |
| **Test weakening by Gemba** | agent loosens a test to make a card go away | commit must cite add/fix/retire evidence; mutation-score check on touched tests; sampled audit; weakening counts against track record; owner review of retirements in v1 |
| **Gemba capture** | Makers pressure Gemba or collude via cards | requests are cards with evidence; decline reasons are recorded; different lineage from Makers; Makers never hold gemba credentials |
| **Over-testing / brittle suite** | coverage-gap cards produce one test per incident | fingerprint dedup; test value analyzer penalizes redundancy and cost; Kaizen delete candidates feed back |
| **Chasing noise** | spurious regressions from low sample size or seasonality | baselines with minimum sample size; mechanical low-confidence gating; false-finding rate tracked against the analyzer, not blamed on the agent |
| **Alert storm / card flood** | many findings, one cause | fingerprint dedup; per-window card budget in the constitution; heijunka sets rate |
| **False andon pulls** | misattributed regression | andon guard verifies evidence; false pulls recorded against Gemba |
| **Stale oracle** | field data old or deploy tags missing, so attribution is wrong | unknown change tags drop the finding; data freshness is a field on every finding |
| **No field data** | pre-deploy dogfood | metrics report "no signal"; Gemba works from spec and issue reports and from the test value analyzer on the repo alone |
| **Starved or absent Gemba** | role sleeps; acceptance changes block | flame minimum for the role (P7); blocked changes are cards, not stalled agents (P11); the owner can still change gated paths directly |
| **Deadlock on the check definition** | check broken, only Gemba/owner can fix | existing owner path stays; andon event escalates to the king |
| **Test-change drift between Gemba and Kaizen** | both touch tests | Gemba owns acceptance meaning; Kaizen proposes deletes as cards that Gemba lands |

## Relation to the existing card

The existing card **"acceptance paths + gemba station"** carries the plumbing:
the constitution's acceptance classification, the guard path rule, and a
station that can hold role `gemba`. This design does not replace it. It
supplies what that card has no spec for: the mandate, the inputs and their
trust handling, the outputs, the metrics, and the failure modes. Concretely:

- Card scope absorbs breakdown items 1-3 below (classification, gating,
  station skeleton).
- Items 4-9 are new and depend on it.
- Whoever implements the existing card should read this document first and may
  split it along the breakdown rather than land it as one change.

## Proposed implementation cards

Ordered by dependency. Each is sized to one Maker change.

1. **Acceptance paths in the constitution.** Define the `acceptance/**` pattern
   convention and scaffold `acceptance/` with one black-box test (e.g. `line
   init` followed by a station API smoke) wired into `just ci`. `line init`
   writes the `guards.paths` entry for role `gemba`. Tests: guard rejects a
   maker commit touching `acceptance/**`; accepts the same under `gemba`.
2. **Gemba station runner.** `sweatshop line gemba`, parallel to `line maker`
   / `line andon`: station keypair and token with role `gemba`, container
   launch, claims `gemba`-family cards. Prompt skeleton encoding the mandate
   and the add/fix/retire rule.
3. **Test-change request flow.** Card type for "Maker requests acceptance
   test change" with required evidence fields, and a decline/accept record that
   lands in the track record. Station API route to file it.
4. **Finding schema and ingestion watcher.** The closed finding schema,
   validation, change-tag verification, dedup by fingerprint, and the card
   emitter. No telemetry source yet: accepts findings from a file or fixture so
   it is testable. Includes the adversarial test: hostile strings in source
   data never appear in card bodies, only behind `evidence_ref`.
5. **Field analyzer sources.** First real source (error/latency telemetry
   with change tags, then issue reports). Baselines, minimum sample size,
   freshness. Blocked on there being a deployed surface.
6. **Gemba metrics.** Field regression rate, SLO burn, acceptance coverage of
   reported issues, as queries over findings, cards and attestations; "no
   signal" semantics. Feeds the heijunka plugin when it exists.
7. **Test value analyzer, Gemba half.** Missing-meaning gaps (field failures
   with no linked acceptance test) and the mutation-score check on touched
   tests used by the integrity gate.
8. **Weakening guard.** A check, required for role `gemba` commits on
   acceptance paths, that fails on removed assertions, skips, or a mutation-score
   drop unless the commit cites retire/fix evidence.
9. **Gemba track record and audit hooks.** Declines vindicated, escape rate,
   spend per closed gap; sampled re-audit of Gemba commits. Depends on the
   agent runtime's track-record store (build order step 4).
10. **Owner-only check definition rule.** Decide and encode whether `justfile`
    and `station/**` stay owner-only once Gemba exists (recommended for v1, see
    [Authority](#authority)), and update the README limitation text when
    cards 1-2 land.

Build-order placement: cards 1-3 and 4 fit the existing slice (the line already
has role-gated paths and witness-attested merges). Cards 5-9 follow the spec's
build order step 7, after spend ledger and heijunka (step 5) and the structure
analyzer (step 6).

## Open questions

- Which surfaces in sweatshop itself count as "field" before a product is
  deployed? Candidates: line operation logs (andon events, failed landings) as
  telemetry about the line itself.
- Should acceptance tests live in a separate repository path or repo, so that
  even a guard bug cannot expose them to Makers?
- Is one Gemba lineage enough, or should acceptance changes need two
  independent Gemba attestations (P8, divergent verification)?
