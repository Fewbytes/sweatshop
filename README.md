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

### 2. Issue Tracking & Workflow (`beads` / `bd`)
Embedded issue tracking with Dolt backend, providing branch-safe, cross-agent task assignment, atomic claims, and structured session close protocols.

### 3. `line` — Assembly Line Primitives
Witness attestations (DSSE/in-toto, Ed25519), the constitution (line rules), and the merge guard that fast-forwards the guarded branch only for trusted, passing attestations. agentsh embeds the witness; `sweatshop` administers keys and the guard's repository hook.

**Limitation:** the witness only protects its key and state from agents that run sandboxed apart from it. In the agentsh dev path (`line_attest`/`line_merge`) the agent and the witness share a host user (trust level `none`), so a malicious agent can read the witness key and alter witness state. The M1 deployment (below) runs the witness on the host in `sweatshop line serve` and the agent in a container. Agent commands are not witnessed yet.

**Limitation:** the witness's clean-room check run executes the candidate's own check definition (`just ci` from its `justfile`, in the image built from `station/`). `line init` gates `justfile` and `station/**` to role `gemba`, and M1 has no gemba station, so only the owner changes them, directly on the host. The tests themselves stay agent-editable: a maker can weaken tests in the commit they judge. Reviewing landed test changes is the operator's job until acceptance paths and a gemba station exist.

*See [`design-docs/assembly-line-spec.md`](design-docs/assembly-line-spec.md)*.

### 4. Agent Skills & Extensions
- **Claude Code**: Agent skills under `.agents/skills/` and `.claude/`.
- **pi.dev**: Extensions and skills compatible with pi agent harness.

---

## Directory Structure

```text
sweatshop/
├── agentsh/           # Go implementation of agentsh daemon, client, and MCP server
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

Host prerequisites: `bd` (beads) on `PATH`, a container engine CLI (`docker`, or a docker-compatible CLI such as `podman`), `just` and Go to build, and host git ≥ 2.45.1 (the witness runs git on the host and refuses older versions).

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

- **Offering work.** Beads labelled `line` that are ready are offered to the Maker, andon cards first; while the line is stopped, only andon cards. The maker claims the bead on the host, runs the agent in a container, and closes it when a commit carrying `Bead: <id>` lands on master during that run; otherwise it unclaims the bead with a note. After `--max-attempts` failed runs (per runner process) the bead is labelled `line-gave-up` and no longer offered; `bd label remove <id> line-gave-up` offers it again.
- **Andon.** `line andon` attests each new master. A failing signed attestation pulls the cord: merges stop except fixes with an `Andon-Event: <id>` trailer, and a P0 bead labelled `line` and `andon` is filed whose description has a line `event: <id>`. While the line stays stopped the watcher keeps one open or in-progress card for the event, filing a fresh one if a fix landed but master still fails. Clearing needs a passing attestation of a master newer than the failing commit; a flake needs one trivial commit carrying the trailer.
- **Containers.** Run with `--cap-drop=ALL` and `no-new-privileges`, the line remote mounted read-only, and the station API reached through `host.containers.internal` (the API listens on loopback only; `line maker --url` overrides).
- **Supervised service.** `line maker --cards api` reaches cards only through `line serve --beads` (`/v1/cards`: ready, claim, release, close, give-up; role `maker`, and only on cards the station has claimed), so the maker never opens `.beads` and needs no `bd`. Its token comes from `SWEATSHOP_LINE_TOKEN` and the API from `SWEATSHOP_LINE_URL`. Run it from an image carrying sweatshop and the podman remote client (no `bd`; the image target and a `just` recipe are not in the tree yet: `justfile` and `station/**` are gemba-gated) as `podman run -d --name sweatshop-maker-1 --restart=always ... --dir LINE --station maker-1` with the station token and model credential as podman secrets, `CONTAINER_HOST` pointing at the podman API socket (so it starts station containers), and the line dir mounted read-only at its host path. The equivalent quadlet (`~/.config/containers/systemd/sweatshop-maker-1.container`):

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
- **Inside a station.** `sweatshop line attest|merge [--rev HEAD] [--repo .]` submit a commit as a git bundle; `line status` shows master and andon state; `line evidence ID` prints a check log.

This path has not been exercised in a live run yet.

---

## Getting Started

1. **Issue Tracking**: Run `bd ready` or `bd show` to inspect work items.
2. **Claude Code**: Ready out-of-the-box with `.claude/settings.json` and `CLAUDE.md`.
3. **pi.dev**: Load skills and extensions via standard pi directory layout or package configuration.
4. **Build agentsh**: `just build` (or `just install` to put it on `PATH`) — see the root `justfile` (`just --list`) and [`packages/agentsh/README.md`](packages/agentsh/README.md).
