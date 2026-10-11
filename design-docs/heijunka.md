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
