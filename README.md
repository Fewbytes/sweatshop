# Sweatshop

**LLM Agent Plugins and Tools for Creating Software Factories**

Sweatshop is a collection of high-performance tools, plugins, execution runtimes, and agent skills designed to build autonomous software factories.

## Targets & Ecosystem Compatibility

1. **Claude Code (Primary)**:
   - MCP servers & tools
   - Hook integrations & workflow definitions
   - Skill definitions & custom commands
   - Drop-in shell runtime replacement (`agentsh`)

2. **pi.dev (Secondary)**:
   - Custom tools & extensions (`.pi/` ecosystem)
   - Skills & prompt templates
   - SDK integrations & custom providers

---

## Core Components

### 1. `agentsh` — Execution Runtime for LLM Agents
A high-performance Go-based daemon (`agentshd`), client CLI (`agentsh`), and MCP server providing isolated, supervised, out-of-context command execution, full command history, process trees, and stream inspection.
*See [`design-docs/agentsh-spec.md`](design-docs/agentsh-spec.md)*.

### 2. `bughunt` — Automated Bug Hunter
Finds bugs, proves each one with a failing test, fixes it, and turns it into a permanent static rule, so `bughunt scan` catches the same class from then on without an LLM. Findings and triage verdicts are tracked in a shared database, so false-positive rates build up across a team. `bughunt scan --diff <rev>` gates commits (`just bughunt-hook` installs the pre-commit hook).
*See [`design-docs/bughunt-spec.md`](design-docs/bughunt-spec.md)*.

### 3. `depmaint` — Dependency Maintenance
Expands a repository's full transitive dependency graph, packages and runtimes alike. It caches the graph with external metadata (versions, advisories, EOL dates, licenses), then runs bounded campaigns: it analyzes what blocks an upgrade, issues work orders to agents and records the verdicts, so nothing analyzed or rejected once is redone. Today it builds and snapshots the graph (`depmaint graph`); campaigns are designed but not built.
*See [`design-docs/depmaint-spec.md`](design-docs/depmaint-spec.md)*.

### 4. `line` — Assembly Line Primitives
Witness attestations (DSSE/in-toto, Ed25519), the constitution (line rules), and the merge guard that fast-forwards the guarded branch only for trusted, passing attestations. agentsh embeds the witness; `sweatshop` administers keys and the guard's repository hook.

**Limitation:** the witness only protects its key and state from agents that run sandboxed apart from it. In the agentsh dev path (`line_attest`/`line_merge`) the agent and the witness share a host user (trust level `none`), so a malicious agent can read the witness key and alter witness state. The M1 deployment (below) runs the witness on the host in `sweatshop line serve` and the agent in a container. Agent commands are not witnessed yet.

**Limitation:** the witness's clean-room check run executes the candidate's own check definition (`just ci` from its `justfile`, in the image built from `station/`). `line init` gates `justfile` and `station/**` to role `gemba`, and M1 has no gemba station, so only the owner changes them, directly on the host. The gate covers the recipe list only, not what the recipes run: `scripts/**` (e.g. `scripts/validate-marketplace.mjs`), the Go sources, and the tests are all outside it, so a maker can change what `just ci` executes without touching `justfile`. The tests themselves stay agent-editable: a maker can weaken tests in the commit they judge. Reviewing landed test changes is the operator's job until acceptance paths and a gemba station exist.

*See [`design-docs/assembly-line-spec.md`](design-docs/assembly-line-spec.md), and [`design-docs/roles-and-responsibilities.md`](design-docs/roles-and-responsibilities.md) for who does what on the line*.

### 5. Agent Skills & Extensions
- **Claude Code**: Agent skills under `.agents/skills/` and `.claude/`.
- **pi.dev**: Extensions and skills compatible with pi agent harness.

### External dependency: beads (`bd`)
[beads](https://github.com/gastownhall/beads) is a separate project, not part of sweatshop. It is a Dolt-backed issue tracker with atomic claims. The line uses it as its kanban: cards, claims and escalations live in beads, `line serve --beads` serves them to stations, and bughunt and depmaint escalate to it. This repository also tracks its own work in beads (`.beads/`). Install `bd` from its repository.

---

## Directory Structure

```text
sweatshop/
├── agentsh/           # Go implementation of agentsh daemon, client, and MCP server
├── bughunt/           # Bug hunter: detectors, rules, findings database
├── depmaint/          # Dependency maintenance: graph expansion, campaigns
├── line/              # Assembly line: witness attestations, constitution, merge guard
├── design-docs/       # Technical specs (agentsh, factory architecture)
├── skills/            # Shared agent skills and tool definitions
├── .claude/           # Claude Code configuration and settings
├── .agents/           # Agent skills (Beads, etc.)
├── AGENTS.md          # Multi-agent system context and rules
└── CLAUDE.md          # Claude Code instructions and quick references
```

---

## Running a line

Design: [`design-docs/m1-line-live.md`](design-docs/m1-line-live.md). The M1 line runs on one host: the witness, guard, line remote and attestation store live on the host; agents run in containers. On macOS the engine is the `docker` CLI against a podman machine; give the VM enough memory for `just ci`.

After the podman machine sleeps or is paused its clock can fall behind (the station's clock-skew alert reports this). `chronyc makestep` does not help then: chrony marks its sources unusable (`^~`/`^?`) and the step is a no-op. Set the clock from the host instead: `podman machine ssh "sudo date -u -s @$(date +%s)"`. Adding `/etc/chrony.d/sweatshop-makestep.conf` in the VM containing `makestep 1.0 -1` (step whenever the offset exceeds 1s, not only during the first 3 updates) does not prevent it: chrony can keep a stale pre-sleep source selected and never step. A host that does not sleep avoids the problem.

Host prerequisites: `bd` from the external [beads](https://github.com/gastownhall/beads) project on `PATH`, a container engine CLI (`docker`, or a docker-compatible CLI such as `podman`), `just` and Go to build. The line itself needs no git program: it reads and writes git repositories in process (go-git).

```bash
just station-image                       # sweatshop-station:dev; agent container and check-runner image
sweatshop line init --dir ~/line --source /path/to/repo [--engine docker] [--image sweatshop-station:dev] [--port 7468]
sweatshop line serve --dir ~/line        # station API on 127.0.0.1:<port>; --addr, --unsafe-listen for non-loopback
```

`init` creates the line directory (it must be empty or absent, and outside any workspace): the line's bare remote seeded from the source's master, the constitution (trust `attest`, check `ci = "just ci"`, `justfile` and `station/**` gated to role `gemba`), and stations `maker-1` and `andon-1`, each with a signing key and a bearer token under `stations/<name>/`.

Run the other stations in their own shells. Client commands and the andon watcher read the station API URL and a station token:

```bash
export SWEATSHOP_LINE_URL=http://127.0.0.1:7468
export SWEATSHOP_LINE_TOKEN=$(cat ~/line/stations/andon-1/token)
sweatshop line andon --beads /path/to/repo [--interval 30s]

export CLAUDE_CODE_OAUTH_TOKEN=...       # or ANTHROPIC_API_KEY; passed through to the container
sweatshop line maker --dir ~/line --station maker-1 --beads /path/to/repo [--once] [--timeout 2h] [--max-attempts 3]
```

- **Offering work.** Beads labelled `line` that are ready are offered to the Maker, andon cards first; while the line is stopped, only andon cards. The maker claims the bead on the host, runs the agent in a container, and closes it when a commit carrying `Bead: <id>` lands on master during that run; otherwise it unclaims the bead with a note. After `--max-attempts` failed runs (per runner process) the bead is labelled `line-gave-up` and no longer offered; `bd label remove <id> line-gave-up` offers it again. A given-up andon card also gets `needs:king`, since the line stays stopped until someone answers it.
- **Gemba.** `sweatshop line gemba --dir ~/line --station gemba-1 --beads /path/to/repo [--once] [--timeout 2h] [--max-attempts 3]` is the same runner for a station of role `gemba`, offered only ready beads labelled `gemba` (a bead needs `gemba` for it, not `line`). Its prompt carries the Gemba mandate and the add/fix/retire rule for acceptance tests (`line/guidance/gemba.md`); it reads cards through `bd` only, not `--cards api`.
- **Andon.** `line andon` attests each new master. A failing signed attestation pulls the cord: merges stop except fixes with an `Andon-Event: <id>` trailer, and a P0 bead labelled `line` and `andon` is filed whose description has a line `event: <id>`. While the line stays stopped the watcher keeps one open or in-progress card for the event, filing a fresh one if a fix landed but master still fails. Clearing needs a passing attestation of a master newer than the failing commit; a flake needs one trivial commit carrying the trailer.
- **Containers.** Run with `--cap-drop=ALL` and `no-new-privileges`, the line remote mounted read-only, and the station API reached through `host.containers.internal` (the API listens on loopback only; `line maker --url` overrides).
- **Supervised service.** `line maker --cards api` reaches cards only through `line serve --beads` (`/v1/cards`: ready, claim, release, close, give-up; role `maker`, and only on cards the station has claimed), so the maker never opens `.beads` and needs no `bd`. Its token comes from `SWEATSHOP_LINE_TOKEN` and the API from `SWEATSHOP_LINE_URL`. `just maker-image` builds its image (`sweatshop-maker`: sweatshop, git and the podman remote client; no `bd`), and `just maker-service [name] [line]` runs it as `podman run -d --name sweatshop-maker-1 --restart=always ... --dir LINE --station maker-1` with the station token and model credential as podman secrets, `CONTAINER_HOST` pointing at the podman API socket (so it starts station containers), and the line dir mounted read-only at its host path. The equivalent quadlet (`~/.config/containers/systemd/sweatshop-maker-1.container`):

  ```ini
  [Container]
  Image=localhost/sweatshop-maker:dev
  ContainerName=sweatshop-maker-1
  Exec=--dir /home/me/line --station maker-1
  Secret=sweatshop-maker-1-token,type=env,target=SWEATSHOP_LINE_TOKEN
  Secret=sweatshop-model-cred,type=env,target=CLAUDE_CODE_OAUTH_TOKEN
  Environment=SWEATSHOP_LINE_URL=http://host.containers.internal:7468
  Environment=CONTAINER_HOST=unix:///run/podman/podman.sock
  Volume=%t/podman/podman.sock:/run/podman/podman.sock
  Volume=/home/me/line:/home/me/line:ro
  SecurityLabelDisable=true

  [Service]
  Restart=always

  [Install]
  WantedBy=default.target
  ```
- **Check runs.** Each check container also gets a unique name (force-removed after the run), and ceilings from the constitution's optional `[runner]` fields `memory` (default `"4g"`), `pids` (`1024`) and `cpus` (`"2"`); `[checks] timeout` (default `"30m"`) bounds each check, and a timed-out check signs nothing. A station runs one attest/merge at a time; a concurrent request gets 409 `{"error":"busy"}`.
- **Inside a station.** `sweatshop line attest|merge [--rev HEAD] [--repo .]` submit a commit as a git bundle; `line status` shows master and andon state; `line evidence ID` prints a check log; `line stats [--since 7d]` prints CI health (pass rate, p50/p95 duration, slow, failing and flaky tests, time to green). The witness runs each check with `GOFLAGS=-json` and parses the `go test` events into the line database `<line>/line.db`, next to the run statistics (`runs.jsonl`, `tests.jsonl`, `kaizen-signals.jsonl` and `heijunka.json` of an older line are imported into it once); runs saved before that still count in the run statistics. `line serve` exposes the same numbers at `GET /metrics` (Prometheus text, station bearer token) for VictoriaMetrics/vmalert; infra failures (container, timeout, cord) are counted apart and never as test failures.
- **Go caches.** Container checks mount the engine volumes `sweatshop-go-mod-cache` and `sweatshop-go-build-cache` at `/cache/go-mod` and `/cache/go-build` (`GOMODCACHE`, `GOCACHE`); nothing is baked into the image. The caches are an optimisation only: warm runs keep `-mod=readonly` but leave `GOPROXY` at its default, so an empty or pruned volume (new host) just downloads what it needs and fills itself, with no priming step. A fully uncached `just ci` took 5m14s (2026-10-18, empty module and build caches), so the default `checks.timeout` of `30m` (never set it lower) is more than 2x an uncached run. `line serve --cold-checks` runs every check with empty throwaway caches instead of the volumes, bounded by `checks.cold_timeout` (default `2h`) instead of `checks.timeout`.

This path has not been exercised in a live run yet.

---

## Getting Started

1. **Issue Tracking**: Run `bd ready` or `bd show` to inspect work items.
2. **Claude Code**: Ready out-of-the-box with `.claude/settings.json` and `CLAUDE.md`.
3. **pi.dev**: Load skills and extensions via standard pi directory layout or package configuration.
4. **Build agentsh**: `just build` (or `just install` to put it on `PATH`) — see the root `justfile` (`just --list`) and [`packages/agentsh/README.md`](packages/agentsh/README.md).
