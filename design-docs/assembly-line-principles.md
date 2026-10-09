# Sweatshop assembly line — principles

A deterministic line operated by stochastic, autonomous agents. Built from first
principles for agents; vocabulary and philosophy borrowed from the Toyota
Production System (TPS).

This document fixes the philosophy. Component specs (witness, guards,
constitution, agent runtime, heijunka, stations, audit) must not contradict it.

## Principles

**P1. Autonomy (promise theory).** Agents promise only their own behavior. No
agent commands another; agents *request*, and recipients may decline. Line
infrastructure is not an agent and does not command either — it only accepts or
rejects actions against shared resources.

**P2. The line is law.** Rules live in the line config (the constitution), set
a-priori by the owner. The framework provides mechanism; config provides policy.
Topologies — dictator, choke point, market, democracy — are consequences of a
particular config, never assumptions of the framework.

**P3. Hard rules at resources, soft rules in agents.** Costly or irreversible
actions (merge, deploy, andon, config change, acceptance-test change) pass
deterministic guards (poka-yoke). Existing toolchains (git, beads, Dolt) already
act as guards; sweatshop adds the missing ones. Everything else — work choice,
priorities, approach — is agent autonomy: measured and audited, not enforced.

**P4. Trust but verify.** Verdicts are mechanical: witnessed exit codes on exact
trees, signed. Never an LLM's report. Verification depth is a CPU-vs-trust knob
in config, from signed attestation alone up to full clean-room re-runs.

**P5. Opposing goals, balanced.** Four goal families pull against each other:

| Goal | Agents |
|---|---|
| features | Makers |
| integrity | Andon |
| simplicity / cohesion | Kaizen |
| fitness for external purpose, meaning of tests | Gemba |

Balance is a leveled *rate of work* (heijunka), not headcount.

**P6. Jidoka.** Defects stop the line where they are found. By default any agent
may pull the andon cord, provided it carries evidence.

**P7. Mortality.** Agents are state with a finite lifetime. They age, die, leave
evidenced testaments, and have children. No immortal agents, no immortal flaws.
Every role keeps the flame: a minimum population always exists, though it may
sleep.

**P8. Exploit non-determinism.** Variation is free mutation and free diversity,
harvested through lineages, speculative parallel attempts, and divergent
verification. Deterministic line, stochastic workers: guards absorb variance,
agents exploit it.

**P9. Minimal serial section (USL).** Serialization happens only at the ref
compare-and-swap; everything else runs concurrently.

**P10. No human legacy by default.** No PRs, no separate CI, no review queues.
Humans enter the line as owner (king) and as escalation target, by config.

## Vocabulary

| TPS term | Line element |
|---|---|
| Jidoka | guards halt on failed checks; no defect passes downstream |
| Poka-yoke | resource guards — hard rules make violations impossible |
| Andon | stop-the-line signal; also the integrity agents |
| Kanban / pull | beads — agents pull work, nobody pushes orders |
| Heijunka | leveling the rate of work across goals |
| Kaizen | simplicity / cohesion agents |
| Gemba / genchi genbutsu | field-fit agents — go see reality, own the meaning of tests |
| Hansei | reflection — learning loops (bug → rule, note → lesson, testaments) |
| Muda / mura / muri | waste (lost CAS races, redundant runs), unevenness (oscillation), overburden (contention, context) — line metrics |
| Respect for people | agent autonomy — request, never command |
