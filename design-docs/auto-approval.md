# Automatic approval of cards: requirements

Research for card sweatshop-ti8u. No code changes. The king asks what must hold
before the line may approve cards without the king's word, with the heijunka
token budget as one pillar.

## What "approval" means today

A card reaches makers only with the `line` label (`GET /v1/cards?ready=true`).
Two things put it there:

1. **The king's `approve <id>`** (text, `/approve`, or a ✅ reaction from the
   king's Discord ID, `line/station/channel_approvals.go`). It turns
   `line-proposed` into `line`. Approving an epic also enters it in the
   `EpicApprovals` ledger (`line/station/epic_approvals.go`).
2. **A viceroy offer inside a delegation** (`POST /v1/cards/{id}/offer`,
   `Server.offer` in `line/station/cards.go`). The constitution's
   `[[viceroy.delegations]]` decides: `constitution.Delegates` covers a card by
   issue `types`, by a `parent` in its ancestor chain, or by `inherit` (any
   non-epic card under an epic the king approved and that is still open). A
   card no delegation covers gets `line-proposed` and waits for the king.

So automatic approval already exists, as a static, king-written whitelist. It
decides by card shape (type, lineage) only. It does not look at the line's
state, the card's content or the cost. "Auto approval in general" means
replacing "this shape is always fine" with "this card is fine now, and
mechanical evidence says so", while the king still owns the rules.

Close has its own delegation (`DelegatesClose`) and is out of scope here.

## Principle

Approval is a promise that the line can afford the card: the work is wanted,
safe to attempt, and the line is healthy enough to absorb it. The king's
authority cannot be inferred (P4: verdicts are mechanical; the viceroy's
charter forbids acting as the king). Therefore auto approval must be
**policy the king signed in the constitution, applied by the station from
facts the station measures itself**. The viceroy and makers may request; they
never supply the facts. Approval stays reversible and bounded: a worst case of
"the line spent some tokens on a card the king would have declined", never
"something irreversible".

## Pillars

| # | Pillar | Question it answers | Status |
|---|---|---|---|
| 1 | Delegation bounds in the constitution | Which kinds of cards may be approved without the king? | exists, coarse |
| 2 | Heijunka token budget | Can the line afford another card now? | meters and gates makers; does not gate approval |
| 3 | Heijunka flow | Is there capacity, or is approval just growing a queue? | exists (`AnalyzeFlow`), not consulted |
| 4 | Andon state | Is the line healthy? | exists, not consulted by offer |
| 5 | Witnessed checks | Will the result be verified without trusting the agent? | exists for code; not tied to approval |
| 6 | Card quality gate | Is the card well formed (acceptance criteria, size, no gated paths)? | missing |
| 7 | Track record | Has this kind of card succeeded before? | partial data, no policy |
| 8 | Revocation and audit | Can the king see and undo what was auto approved? | partial |

### 1. Delegation bounds (exists)

`Delegation` is the single place where the king grants approval power
(`line/constitution/constitution.go`). It is sovereign-gated: constitution
changes need `approve constitution N`. Gaps:

- Matching is only by type, parent or approved-epic lineage. Labels are not
  allowed for offers on purpose (any agent can add a label), which is right.
- No conditions: a delegation is on or off. There is no per-delegation cap on
  cards in flight, tokens per card, or approvals per period.
- No distinction by author. A card filed by the viceroy from the king's intent
  and a card filed by a maker or kaizen run are treated alike by `types`.
  Auto approval should know the card's origin (the station knows the token's
  role that created it) and bound how far machine-filed work may approve
  machine-filed work (a loop risk, see below).
- Every offer is recorded as an event (`EventOffer`), but not *which*
  delegation allowed it, so audit cannot tell which rule to tighten.

### 2. Heijunka token budget (exists, wrong end)

`[[budgets]]` (`line/constitution/budget.go`, `line/station/budget.go`,
`design-docs/heijunka.md` "Token budgets") meter self-reported usage over a
rolling window per scope (`line`, `card`, or a role). Exhaustion hides cards
from `ready=true` for makers and refuses claims with 429; running work
continues. Usage ageing out lifts it.

For approval this is the main affordability signal, but today it acts after
approval: an approved card just sits unoffered. That is safe, yet it means a
backlog of approved cards piles up exactly when the line is poor, and the
approval itself carries no cost estimate. Needed:

- **Approval-time headroom check.** `offer` should auto approve only while the
  `line` scope (and the role scope for the likely role) has headroom below an
  approval threshold, lower than exhaustion (for example under
  `warn_percent`). Above it the card is labelled `line-proposed` as today, so
  the king sees it instead of it silently queueing. `budgetBlock` already
  computes the inputs; a new pure function over `BudgetStatus` would decide.
- **Per-card cost bound.** A `card` scope budget exists, but usage reporting is
  self-reported by stations and the maker runner does not yet report per run
  (heijunka.md: "follow-up work"). Without reliable usage, headroom is a
  fiction and auto approval would run on stale numbers. Reliable metering (a
  proxy, or the runner reporting each run) is a **precondition**, not a
  refinement. An unmetered or silent line must fail closed: no headroom data
  means no auto approval.
- **Expected cost.** Budgets cap but do not estimate. A first estimate can be
  the median tokens of closed cards of the same type from `linestore` usage;
  it only needs to refuse a card whose estimate exceeds remaining headroom.

### 3. Heijunka flow (exists, not consulted)

`heijunka.AnalyzeFlow` reports WIP, wait per stage and the bottleneck. If the
bottleneck is `needs:king` or `queued` with a long wait, approving more cards
adds latency without throughput. Auto approval should respect a WIP-style
bound: do not auto approve when approved-but-unclaimed cards already exceed a
multiple of the maker count (the levels from `GET /v1/heijunka/levels`). This
keeps leveling (heijunka) honest: approval is the line's intake and should be
paced by capacity, not by how fast cards are filed.

### 4. Andon state (exists, not consulted)

While the cord is pulled, the merge guard rejects landings, so approving new
work only builds a queue (heijunka already drops the WIP limit to 1). Auto
approval must pause while the cord is pulled in any scope, except for the fix
card the andon station files, which is the one thing the cord asks for. The
station already knows cord state; `offer` needs one more input. Falling back to
`line-proposed` is correct.

### 5. Witnessed checks (exists)

The reason auto approval is safe at all: a landed change is gated by signed
attestations, required checks in a clean container, path guards (gated paths
only for their role), and blob limits. Whatever is approved cannot change the
constitution, the justfile or `acceptance/**`, or land without green checks.
Requirement: the delegation must never cover a card whose likely change needs
a gated path or a role the maker lacks. That is enforced at merge, so the
worst case is a stuck card, but a card quality gate (pillar 6) can refuse it
earlier and save the tokens.

Residual risk the checks do not cover: work that is merged and green but
unwanted (wrong direction, large refactor, new dependency). Auto approval
scope should therefore be limited to categories where "green and merged" is
close to "done" (bug, chore, test, docs), which is what the existing `types`
delegations do.

### 6. Card quality gate (missing)

Cards are free text. Approval should require, checked mechanically by the
station at offer time:

- a non-empty `acceptance_criteria` field and a description;
- a size bound (description length; no more than N dependencies);
- not a duplicate of an open or recently closed card title (cheap similarity);
- not an epic (already enforced: `EpicType` never inherits).

This guards against the viceroy or a maker approving a vague card that burns a
budget on guesswork. It must fail with a reason that goes onto the card as a
comment, so the filer can fix it.

### 7. Track record (partial)

`line/cistats` and the line event store already hold per-run CI pass rates,
flakes and time to green, and the event log records give-ups (`line-gave-up`
with a reason comment) and released cards. What is missing is a policy that
reads them. Useful signals, all derivable from the station's own records:

- recent give-up/park rate of cards in the delegation's category;
- recent pass rate of the required checks on master (a broken or flaky master
  makes any approval wasteful, which overlaps with andon);
- whether the delegation has recently produced reverted merges.

A delegation with a poor record should degrade automatically to
`line-proposed` (ask the king) rather than keep approving; it recovers when the
rate does. The thresholds are constitution values, so the king sets them.

### 8. Revocation and audit (partial)

- Revocation exists in pieces: epic approval is dropped when the epic closes
  (`revokeEpicApproval`), the king can `defer <id>`, and `line-gave-up` parks a
  card. The king can remove a delegation by a constitution change.
- Missing: a king-visible digest of what was auto approved ("12 cards approved
  by delegation X in 24h, 3 given up"), an emergency stop that disables all
  auto approval without a constitution round trip (the `defer`-style word for
  the whole line, for example `hold approvals`), and the delegation id on each
  offer event.

## Risks

- **Self-feeding loop.** Makers and kaizen file cards; if those are
  auto approved, the line can generate and approve its own work without end.
  Needs a cap on machine-filed approvals per period and a rule that cards filed
  by an agent role are never covered by a delegation that agent's own output
  could satisfy (distinguish author role, pillar 1).
- **Label and text gaming.** Any agent can set labels and write criteria. Use
  card type, lineage and station-observed facts; never labels or card text as
  the grant. This is why delegation `labels` are close-only today.
- **Stale or forged usage numbers.** Budgets rely on self-reported tokens. A
  station that under-reports keeps the headroom open. Mitigate by metering at a
  proxy, and by failing closed when reports stop.
- **Check-then-act races.** Headroom is read, then the label is added. Two
  concurrent offers can both pass at the threshold. The overshoot is bounded by
  the per-period approval cap; making the check and label atomic in
  `linestore` would remove it.
- **Authority drift.** Policy inputs (thresholds, caps) are constitution
  values, so they change only with the king's approval. The viceroy may
  propose, as it does for delegations today.
- **Reduced visibility.** If everything is auto approved, the king stops
  seeing the line. The digest in pillar 8 is part of the requirement, not an
  extra.
- **Fail-closed cost.** Each new input is another reason to fall back to
  `line-proposed`. That is the safe direction, but a flapping signal (for
  example a budget oscillating at the threshold) would make approvals
  inconsistent; use the existing hysteresis style (warn once, lift by itself).

## Proposed order of work

Each step is one card, ships with tests at the station port level (testing
guide), and changes nothing for the king until a constitution turns it on.

1. **Reliable usage metering.** The maker runner reports each run's tokens;
   station treats missing reports as an unmetered line. (Precondition for
   everything that uses budgets. Already named follow-up work in heijunka.md.)
2. **Record the delegation that allowed an offer** in the offer event, and
   expose it in status. Cheap, enables audit and later tuning.
3. **Offer gates, as pure functions over station state:** cord pulled,
   budget headroom, approved-unclaimed WIP bound. Failing a gate yields
   `line-proposed` plus a comment naming the gate. Inputs on a new
   `[viceroy.delegations]` condition block (`max_budget_percent`,
   `max_unclaimed`), absent meaning today's behavior.
4. **Card quality gate** at offer time.
5. **Per-delegation approval cap per period and author role rule**, which
   closes the self-feeding loop.
6. **Track record degradation** reading cistats and give-up rates.
7. **King digest and `hold approvals` stop word.**
8. Only then widen delegations (more types, `inherit` epics) as the king
   chooses; each widening is a constitution proposal like any other.

Steps 1 to 3 are enough to let the king safely turn on today's type
delegations with heijunka-aware pacing. Steps 4 to 7 are what "in general"
needs before delegating anything beyond bugs and chores.

## Open questions for the king

- Which categories, if any, should never auto approve whatever the record
  (new dependencies, anything touching `line/` itself, deploys)?
- Should the budget threshold for approval be one line-wide number, or per
  delegation?
- Is a failing give-up rate in a category grounds to disable its delegation
  automatically, or only to alert?
