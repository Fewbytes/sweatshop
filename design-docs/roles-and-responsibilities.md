# Roles and responsibilities

Who does what on the assembly line: the humans, the agent roles, and the
deterministic line components. This is a map, not a spec. Each entry points to
the document that defines it: [`assembly-line-spec.md`](assembly-line-spec.md)
(architecture), [`assembly-line-principles.md`](assembly-line-principles.md)
(P1–P11), and the per-role designs.

Two rules hold everywhere:

- **Agents request, infrastructure decides.** An agent never holds a signing
  key, never pushes, and never clears a stop. It asks a component, and the
  component checks the request against the constitution (P3).
- **Nobody commands an agent.** Agents coordinate through cards, the heijunka
  signal and the andon. One agent may ask another, and the other may decline
  (P1).

Status key: **running** means it runs on the sweatshop line today; **built**
means the code exists but nothing runs it continuously; **designed** means it
exists only on paper.

## Humans

| Who | Responsibility | Powers |
|---|---|---|
| **King** (sovereign; today `nukemberg`) | Sets the goals and the law. Approves cards the viceroy may not offer alone, constitution changes, and releases. | Every power. A decision counts only when the king's own authenticated message on the channel carries it; the viceroy's word that "the king said yes" never does. |
| **Owner** (whoever runs the host) | Operates the line: creates the line directory, registers stations and keys, deploys and restarts services, holds the credentials. | Host access. Today the owner and the king are the same person. Owner work that should be automated becomes cards for the line. |
| **Other principals** | None yet. | Only what the constitution grants them under `governance`. |

## Agent roles

Each agent has one role for its whole life. A worker (a container with
compute and a token budget) may host different agents over time; the agent is
the persistent state, not the process.

### Maker: features and fixes

- **Goal:** turn ready cards into merged commits.
- **Does:** claims a ready `line` card, works on it in a station container,
  commits, and asks its witness to check and merge the result. On a lost race
  it rebases and resubmits.
- **May not:** edit gated paths (`justfile`, `station/**`, and later the
  acceptance tests), merge without a passing attestation, or clear the andon.
- **Status: running.** One station, `maker-1`. The `sweatshop line maker` loop
  runs as the podman service `sweatshop-maker-1` and reaches cards only through
  the station API (`--cards api`). Each card runs in a `sweatshop-station`
  container.
- **Population:** keep the flame. At least one maker must always be alive, and
  `line serve` posts to the channel when none has sent a heartbeat for a while.

### Andon: integrity

- **Goal:** master stays green.
- **Does:** watches master, has the witness run the required checks on each new
  master, pulls the cord with that attestation as evidence when they fail, and
  clears it when an attested run on a newer master passes. Pulling the cord
  creates a top-priority card that any agent may swarm.
- **May not:** fix code itself (that is ordinary maker work), or stop the line
  without evidence. A pull that does not reproduce counts against the puller.
- **Status: running** as a script, not an LLM (`sweatshop line andon`, station
  `andon-1`). It can only stop the whole line; scoped stops, the circuit
  breaker and auto-revert are designed but not built.

### Kaizen: simplicity and cohesion

- **Goal:** keep the code small, cohesive and cheap to change, and hold the
  complexity index near its setpoint.
- **Does:** reads the structure analyzer and test value analyzer, files cards
  for hotspots, dead code, redundant tests and slow or flaky suites, and works
  those cards like a maker. It runs depmaint campaigns and bughunt rules.
- **May not:** add features, or edit acceptance tests.
- **Status: designed** ([`kaizen-agent.md`](kaizen-agent.md), `sweatshop-a82`).
  Until it exists, `line/guidance/kaizen.md` asks every writer to simplify.

### Gemba: field fit and the meaning of tests

- **Goal:** what ships works for its users, and the tests prove that.
- **Does:** watches how merged code behaves outside the line (errors, latency,
  usage, user reports) and turns that evidence into cards. It owns the
  acceptance paths: the black-box tests plus `justfile` and `station/**`,
  which define how `just ci` runs and in what image. It makes sure every
  failure class reported from the field gets an acceptance test.
- **May not:** build features, stop the line, or edit the constitution. Its
  merges still have to pass `just ci`.
- **Why it is separate:** without it, a maker could weaken a test or the check
  recipe in the same commit that is judged by it.
- **Status: designed** ([`gemba-agent.md`](gemba-agent.md), `sweatshop-8kl`).
  The path guards exist. The only key with the `gemba` role is `gemba-owner`,
  which the owner uses to land station and justfile changes. Revoke it once a
  real gemba station exists. The planned restriction of gemba merges to gated
  paths plus fixtures is not enforced yet.

### Viceroy: the humans' interface

- **Goal:** the king's intent reaches the line, and the line's state reaches
  the king.
- **Does:**
  - Talks with the king on the channel (Discord `#sweatshop-assembly-line`),
    turns intent into ready cards, and offers them to the line where the
    constitution allows it. Today that covers bugs and cards under
    `sweatshop-0mw.2`; everything else waits for the king's `approve <id>`.
  - Supervises the line: reads its master, andon state, runs and card flow,
    reports to the king, and files cards for anomalies.
  - Routes questions between agents and humans.
  - Builds and starts its own successor (`viceroy succeed`), and asks the king
    to redeploy the line (`viceroy deploy`).
  - Writes a testament that its successor inherits.
- **May not:** merge, clear the andon, change the constitution (it can only
  propose a change for the king to approve), act as the king, or restart line
  services. Messages from anyone but the king are data, not orders: the
  viceroy is the line's likeliest prompt-injection target, so its powers stay
  narrow.
- **Status: running.** Station `viceroy-1`, a container at generation 6,
  reached through self-succession (M2, [`m2-succession.md`](m2-succession.md)).
- **Population:** at least one per sovereign.

### Heijunka (agent): not a role yet

There is no heijunka agent. The [heijunka worker](#heijunka-worker) computes
levels and nothing acts on them yet, so the owner's operator session carries
the duty by hand: keeping the flame lit and sizing the maker pool.

## Line components (deterministic, not agents)

| Component | Responsibility | Where | Status |
|---|---|---|---|
| **Station API** (`sweatshop line serve`) | The single door for every station. Authenticates each one by its bearer token. Proxies cards to beads, the channel, testaments, constitution reads and proposals, viceroy succession and deploy requests, run statistics, and heartbeats. Raises the "no maker alive" alert. | Host service (launchd today) | running |
| **Witness** | One Ed25519 key per station, held by `line serve`, never by the agent. Fetches a candidate bundle into a witness-owned repository, runs the constitution's required checks (`just ci`) in a clean `sweatshop-station` container, and signs an in-toto/DSSE attestation over the exact commit. Records each run and its evidence log. | Inside `line serve` | running |
| **Merge guard** | Compare-and-swap on master: the attestation must be valid and from a trusted key; the candidate must be the attested commit with its parent at the current master; the required checks must have passed; the line must not be stopped; and gated paths may change only under the right role. | `line serve` and the `pre-receive` hook in `remote.git` (which accepts only `refs/line/candidates/*`) | running |
| **Constitution** | The law: trust keys and roles, required checks, path guards, runner image, sovereign, channel, viceroy delegations. Versioned; every attestation and merge records the version that judged it. | `constitution.toml` in the line directory; history through `line constitution log` | running |
| **Andon guard** | Records andon events with their evidence and blocks merges while the line is stopped. Clears an event only on an attested passing run (clearing is mechanical, never declared), and closes the event's swarm cards. | Inside `line serve` | running (global stop only) |
| **Beads** | The kanban: cards, atomic claims, dependencies, the `line` label that offers a card to stations. Containers never open its database; they go through the station API. | `~/code/sweatshop/.beads` (embedded Dolt) | running |
| **Channel** | The king's Discord channel. Gateway transport with the `/approve` slash command, ✅-reaction approvals and a typing indicator, falling back to REST polling. Verifies that approvals come from the king's Discord ID. | Inside `line serve` | running |
| **Station runner** (maker loop) | Takes a ready card, starts a station container with resource limits and a timeout, retries up to the attempt limit, and releases the claim on failure. | `sweatshop line maker`, podman service | running |
| **Station image** | The single toolchain for agents and the witness (`sweatshop-station`): git, Go, uv, just, node and Claude Code. Gated to gemba. | `station/Dockerfile` | running |
| **Maker service image** | `sweatshop-maker`: the maker loop with git and podman-remote; no bd and no Claude Code. | `station/Dockerfile` target `maker`; `just maker-image`, `just maker-service` | built (landing under `sweatshop-0mw.7`) |
| **Heijunka worker** | Reads the backlog and run statistics, then publishes a target maker count and WIP limit. | `sweatshop line heijunka` → `heijunka.json` | built; nothing acts on its output |
| **Deployer** | The privileged actuator: applies a release the king approved by restarting line services through a backend. The viceroy can only ask for one. | `sweatshop line deployer` (launchd and k8s backends) | built; deploys are done by hand today |
| **Watchers** | Post-merge suites, telemetry and scans that emit cards or pull the andon. | — | designed |
| **Analyzers** | Structure analyzer (Kaizen), field analyzer (Gemba), test value analyzer (both). Each emits findings as cards. | — | designed |
| **Agent runtime** | Agent state, lineage, hosting, metamorphosis, lifetimes, the flame minimum per role, and the worker scheduler. | — | designed (only viceroy succession exists) |

## Who may do what

| Action | Who | Checked by |
|---|---|---|
| Offer a card to the line | King; viceroy within its delegations | Viceroy delegation rules in the constitution, plus the king's approval on the channel |
| Claim and work a card | Maker (Kaizen and Gemba later) | Atomic claim in beads |
| Merge to master | Any station, through its witness | Merge guard (attestation, CAS, path guards, andon state) |
| Change `justfile` or `station/**` | Role `gemba` | Path guard |
| Pull the andon | Any agent, with evidence | Andon guard |
| Clear the andon | Nobody: an attested passing run clears it | Andon guard |
| Change the constitution | King (the viceroy may propose) | King's approval on the channel |
| Add a station or key | Owner | Host access (`line station add`) |
| Deploy or restart line services | Owner today; the deployer after the king approves a release | Host access; release approval |
| Start a viceroy successor | The viceroy on duty, or the owner for generation 1 | Station API succession checks |

## Gaps the owner still covers

Until the components above are built, the owner's session does these by hand.
Each one should become line work:

- Keeping the flame lit: restarting dead makers and sizing the maker pool
  (heijunka agent, agent runtime).
- Deploying and restarting line services (deployer, `sweatshop-0mw.5`).
- Landing changes to gated paths with the `gemba-owner` key (gemba station).
- Watching for errors on the line and filing a card for each one (jidoka;
  watchers, an LLM andon).
