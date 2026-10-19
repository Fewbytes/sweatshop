# Heijunka worker

Levels the line's flow by adjusting capacity: the number of maker agents and
the kanban WIP limit (cards in progress at once).

## Decision: our own daemon that polls state

Options considered:

| Option | Verdict |
| --- | --- |
| k8s CronJob | Periodic only; no manual or event wake, and the line also runs on launchd. |
| k8s controller | Right shape for k8s, but needs a CRD and ties levelling to one backend. |
| system cron | Periodic only; one process per wake, no poke. |
| own daemon (`sweatshop line heijunka`) | Same pattern as `line andon`; one loop, any backend. **Chosen.** |

Events: no new bus. Every wake re-reads the line's state (card counts from
bd, andon state from the station), so a missed event costs at most one
interval and nothing is lost on restart. Triggers map to:

- periodic: the ticker (`--interval`, default 5m);
- manual: SIGUSR1 pokes the loop;
- no pending cards / large backlog: not wake sources but inputs to `Decide`,
  which scales down toward `--min-agents` or up toward `--max-agents`;
- line events (card filed, andon pulled): seen on the next wake. A purpose
  built bus can later feed the same `Poke` channel without changing `Decide`.

## Levelling rules (`heijunka.Decide`, pure)

- Target agents = ceil((ready + in progress) / 2), raised to one above the
  current count when a ready card waits past the latency target, clamped to
  the policy's bounds; agents move at most one per wake so bursts do not
  flap the fleet.
- WIP limit = agents x 1.
- Andon pulled: WIP limit 1 (only the fix), agents unchanged.

## Flow metrics and the bottleneck

Heijunka serves flow (JIT: low latency, balance); rate is secondary and never
the sole basis of a decision. `heijunka.AnalyzeFlow` (pure) reads the line's
cards and reports, each wake:

- per stage, WIP, total wait and the oldest card with its wait. An open card
  is in `queued` (ready, no maker yet, since created), `working` (claimed,
  since started) or `needs:<role>` (waiting on that role, since the card's
  last update). Parked cards (`line-gave-up`) are off the line;
- the bottleneck: the waiting stage (never `working`) with the largest total
  wait, with the evidence above. A growing `queued` means makers are short;
  `needs:king` means approval is the constraint, which more makers cannot fix;
- mean lead time (created to closed) and mean queue latency (created to
  claimed) over cards closed in the last 7 days.

`Decide` uses queue latency: a ready card waiting past
`Policy.QueueLatencyTarget` (1h) asks for one more agent than are running,
within the usual bounds and step. The worker logs the bottleneck every wake.

Not observable from cards, so not separated yet: time on the witness or the
merge guard (both happen inside `working`). Surfacing them needs station
events and is follow-up work.

## Actuation

Levels are published through the station API (`POST /v1/heijunka/levels`,
heijunka role) and kept in the line database `<line dir>/line.db`. Whatever
runs makers (supervisor, k8s deployer scaling a Deployment) reads them with
`GET /v1/heijunka/levels` (`{"agents":N,"wip_limit":M}`); wiring those
consumers is follow-up work. No `heijunka.json` is written; an actuator that
cannot call the API would need an export from `line serve`. A `heijunka.json`
left by an older line is imported into the database once.

## Running as a service

`Worker` (`line/heijunka`) knows nothing about its host: it takes an `Observer`,
an `Actuator` and a `Poke` channel. `sweatshop line heijunka` is the service
host (flags, signals, bd and station-API observer). A k8s controller would
be another host that supplies its own observer and actuator and reuses
`Worker` unchanged; it is a separate card.

On the server, with systemd (`line/deploy/systemd/sweatshop-heijunka.service`):

1. Install the `sweatshop` binary (`just install`) and make sure `bd` is on
   the service's `PATH`; the observer and the needs triage call it.
2. Run the station API (`line serve`) and give the worker a station token:
   put `SWEATSHOP_LINE_TOKEN=<token>` in the unit's `EnvironmentFile`.
3. Copy the unit to `~/.config/systemd/user/`, fix the paths, then
   `systemctl --user daemon-reload && systemctl --user enable --now sweatshop-heijunka`.
   `loginctl enable-linger $USER` keeps it running without a login session.
4. Check: `journalctl --user -u sweatshop-heijunka` shows level changes, and
   `GET /v1/heijunka/levels` returns the current levels.

`Restart=always` restarts it on failure; state is re-read on every wake, so a
restart loses nothing. Poke with
`systemctl --user kill -s USR1 sweatshop-heijunka`.

## Kaizen runs

Kaizen runs on its own timer; heijunka can also ask for a run when the line
has spare capacity (`heijunka.ShouldRequestKaizen`, pure): the cord is not
pulled, no card is ready, a maker is idle, a kaizen station is idle and its
last run began over `KaizenMinGap` (6h) ago, and no request was made on the
current master. It asks with `POST /v1/kaizen/request` (heijunka role); the
line refuses while the cord is pulled or a request is pending. The kaizen
runner sees the pending request in `GET /v1/status` and answers with
`POST /v1/kaizen/answer` (kaizen role): `started`, or `declined` with a
reason. A request replaces only the runner's interval; its master-moved and
andon guards still apply. Because the answered request keeps the master it was
made on, a decline is not asked again until master moves.

## Stale needs:<role> cards

A card labelled `needs:<role>` that nobody touches can deadlock. Each wake the
worker lists such cards not updated for `cards.needs_timeout` (constitution,
default 24h) and applies `heijunka.DecideStaleNeed`:

- `needs:king` unanswered: park it (label `line-gave-up`, drop the need) so
  makers stop reopening it; an operator removes the label to retry.
- `needs:viceroy` unanswered: escalate to `needs:king`.
- any other role: reroute to `needs:viceroy`.

Each action comments on the card, which restarts the clock. Splitting a card
needs judgment about its content, so the viceroy does it after a reroute.

## Token budgets

Spend is a leveled, visible quantity like flow. Policy lives in the
constitution; the line only meters and gates.

```toml
[[budgets]]
scope = "line"       # "line", "card" (each card separately) or a role such as "maker"
tokens = 5000000
period = "24h"       # rolling window, not a calendar day
warn_percent = 80    # optional, 1..99, default 80
```

- Station containers are meant to report automatically. `sweatshop line usage
  --card ID --claude-output FILE` prints the text of a `claude -p
  --output-format json` result and reports its input, output and
  cache-creation tokens (`POST /v1/usage`); kaizen reports under its run ID.
  Usage lands when a run ends, so a budget is checked per run, not per turn.
  Nothing calls it yet: `station/**` is gemba-only, so gemba must change the
  last lines of `station/entrypoint.sh` to

  ```sh
  claude_out=$(mktemp)
  claude_status=0
  claude -p "$SWEATSHOP_PROMPT" ... --output-format json >"$claude_out" || claude_status=$?
  sweatshop line usage --card "$SWEATSHOP_BEAD" --claude-output "$claude_out" ||
      echo "entrypoint: token usage not reported" >&2
  exit "$claude_status"
  ```

  (`...` are the existing flags). Until then, stations may still self-report
  with `--tokens N`.
  The line takes station and role from the token, so a station cannot report
  for another. A station's own script could in principle skip the report; a
  proxy would meter without trusting the station, and reports are the stable
  interface it can feed later.
- Exhaustion pauses offers, never work: makers get no ready cards from
  `GET /v1/cards?ready=true` in an exhausted scope, a claim there is refused
  with 429, and running work keeps going and keeps reporting.
- The channel is told once when a budget crosses `warn_percent` and once when
  it is exhausted. Usage ageing out of the window lifts the pause by itself.
- `sweatshop line status` (`GET /v1/status`, `budgets`) shows used, remaining
  and exhausted per budget.
- Budgets are part of the constitution, so changing them takes the king's
  approval like any other proposal.
- Proposed initial budget, for the viceroy to propose and the king to approve
  (a line-wide ceiling that warns before it pauses; tune from `line status`):

  ```toml
  [[budgets]]
  scope = "line"
  tokens = 20000000
  period = "24h"
  warn_percent = 80
  ```
