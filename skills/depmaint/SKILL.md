---
name: depmaint
description: Drive a depmaint dependency-upgrade campaign. Use when told to run depmaint, advance a campaign, or execute a depmaint work order.
---

# depmaint campaign loop

depmaint holds the state and analysis; you execute one work order at a time
and report what happened. "Run depmaint and follow its output" means this loop.

1. `depmaint campaign list` to find the campaign id (or `depmaint campaign new --target ecosystem:name=version`, then `depmaint campaign plan <id>`).
2. `depmaint campaign next <id>`. It prints a state:
   - `dispatch`: read the work order file it names and do exactly that step.
   - `succeeded`: stop, the goal is reached.
   - `exhausted` or `blocked`: go to step 5.
3. Execute the work order: make the edit it describes, run its verification (build, tests, `bughunt scan`).
4. Report with the `depmaint campaign record <id> --step <n> --verdict ...` command printed at the end of the order:
   - `succeeded` only when verification is green; add `--pr <url>` if you opened one.
   - `rejected` when the upgrade is wrong on its merits (removed API, incompatible license). `--reason` is required and is remembered: the same step is not re-issued until the graph or plan changes.
   - `failed` for anything transient (network, flaky test). The step may be tried again.
   Then return to step 2. Never edit the database or work orders by hand.
5. Stalled on a human decision (blocked, or budget exhausted)? Run `depmaint escalate <id> --reason "..."`. It writes `.depmaint/reports/<id>.md`, which is the artifact to commit and hand over, and files a bead that points at it when beads is available. If it says beads is unavailable, tell the user directly and name the report path.

`depmaint campaign report <id>` re-renders the report at any time.
