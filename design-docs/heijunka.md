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

- Target agents = ceil((ready + in progress) / 2), clamped to the policy's
  bounds; agents move at most one per wake so bursts do not flap the fleet.
- WIP limit = agents x 1.
- Andon pulled: WIP limit 1 (only the fix), agents unchanged.

## Actuation

Levels are published atomically to `<line dir>/heijunka.json`
(`{"agents":N,"wip_limit":M}`). Whatever runs makers (supervisor, k8s
deployer scaling a Deployment) reads it; wiring those consumers is follow-up
work.

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
   `<line dir>/heijunka.json` holds the current levels.

`Restart=always` restarts it on failure; state is re-read on every wake, so a
restart loses nothing. Poke with
`systemctl --user kill -s USR1 sweatshop-heijunka`.

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
