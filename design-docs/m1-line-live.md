# M1 — Line live

Milestone 1 of the assembly line: the spec's *first slice* (end of build step
3). The sweatshop line builds sweatshop with no human merging. Epic:
`sweatshop-9px`. Parent spec: `assembly-line-spec.md`; principles:
`assembly-line-principles.md`.

## Exit criteria

1. `sweatshop line init` creates a line: constitution v0, the line's bare
   remote, guard hooks, and one witness key + station token per station.
2. Agents run inside Linux containers. Witness, keys, the line remote and the
   attestation store are outside the container. Trust level `attest` is on.
3. A Maker station runs an LLM agent in a container: it gets a ready bead,
   works, attests and merges.
4. An Andon station attests each new master. A failing, signed attestation
   pulls a global stop; the guard then accepts only fixes that reference the
   event; a passing attestation of the new master clears it.
5. The harness covers claim → merge → break → andon → fix → clear; one real
   bead lands on sweatshop through the line.

Out of scope: human channel (`sweatshop-u07`), build steps 4+ (agent runtime,
heijunka, Kaizen/Gemba, lifetimes), scoped andon, circuit breaker auto-revert.

## Trust domains

```
 host (operator uid)                         container (agent)
 ┌──────────────────────────────────┐        ┌─────────────────────────┐
 │ sweatshop line serve             │  HTTP  │ claude -p (Maker)       │
 │  ├ station witnesses (key each)  │◀───────│ git clone of master     │
 │  ├ guard: merge / andon / clear  │ token  │ sweatshop line merge    │
 │  ├ line remote (bare, hooks)     │        │  (bundle → POST)        │
 │  └ evidence + attestation store  │        └─────────────────────────┘
 │ check runs: docker run <image>   │        line remote mounted read-only
 │ station runners (maker, andon)   │
 └──────────────────────────────────┘
```

- **The witness leaves agentshd.** Witnessing is line infrastructure, not an
  agent tool. `sweatshop line serve` hosts every station's witness on the host.
  agentshd stays the agent's shell tool, inside the container if used at all,
  and untrusted. Its former `line_attest`/`line_merge` dev ops are gone. This resolves `sweatshop-qp5`: no agent
  command ever runs as the witness's uid.
- **Station API.** HTTP on `127.0.0.1:<port>`. Containers reach it through the
  engine's host alias (`host.containers.internal`; verified with podman
  machine). Each station has a bearer token; the token selects the station, and
  the station fixes role, agent ID and key. A stolen token is only that agent's
  own identity. Production Linux hosts may use a unix socket mount instead.
- **Candidate transfer is a git bundle** (`sweatshop-xw8`). The client bundles
  `HEAD ^<line master>` and POSTs it. The witness verifies the bundle against
  its cache (seeded from the line remote, which it trusts) and fetches the
  commit from the bundle file (the bundle carries one ref under
  `refs/sweatshop/`; the witness fetches `+refs/sweatshop/*:refs/bundle/<nonce>/*`
  and requires the commit to be reachable from it). The agent's repository and its config are never
  read.
- **Agent commands are not witnessed in M1.** Trust rests on the witness's own
  clean-room check run. Agent command logs are untrusted evidence. Host-side
  witnessing of agent commands is deferred.
- **The candidate defines its own check.** The witness runs `just ci` from the
  candidate's `justfile` in the station image built from `station/`. `line init`
  gates `justfile` and `station/**` to role gemba; M1 has no gemba station, so
  only the owner changes them, directly on the host (such changes cannot go
  through the line in M1). **Residual limitation:** the tests themselves are
  agent-editable — a maker can weaken or delete tests in the commit they judge.
  Reviewing landed test changes is the operator's job until acceptance paths
  and a gemba station exist.
- **Check runs are bounded.** Each check container runs with
  `--cap-drop=ALL`, `no-new-privileges`, a unique name force-removed after the
  run (also on cancel), and resource ceilings from the constitution's optional
  runner fields `memory` (default `"4g"`), `pids` (1024) and `cpus` (`"2"`). The
  ceilings are not part of the runner ID, so tuning them keeps existing
  attestations valid. Maker containers get the same ceilings by default
  (`sweatshop line maker --memory/--pids/--cpus`). A check that exceeds
  `checks.timeout` (default `"30m"`) or is cancelled is a runner error: nothing
  is signed. So are engine failures: exit 125 (the engine could not start the
  container) and exit 137 unless `docker inspect` reports `OOMKilled=true`
  (a kill by the engine or VM is not the check's fault; an OOM kill by the
  check's own `--memory` limit is a genuine failure and is signed). The station
  API runs one attest/merge per line at a time across all stations (409
  `{"error":"busy"}`). **Not guaranteed:** disk is not limited, so an agent can
  still fill the VM's disk and make a check fail for real (signed), and
  resource ceilings do not stop other host-level starvation (e.g. many maker
  containers together exceeding the VM). Treat a pulled andon as a claim to
  verify, not proof. Check containers run as the
  image's user (`agent`, uid 1000, in the station image).

## Attestations

- The witness signs **failing** runs too. Andon needs signed evidence of
  failure. The guard already rejects a failing attestation for merge
  (`checks_failed`), so signing failures does not widen merge authority.
- Statement unchanged otherwise: commit-bound subject, role, agent ID,
  constitution digest, runner ID, check results.

## Andon (global stop only)

- **Pull.** `POST /andon/pull` with a signed attestation whose commit is the
  current line master and which records a failing required check. The guard
  writes the event as a blob in the line remote and points `refs/line/andon` at
  it (create-only CAS). Event = {commit, failing check, puller agent ID + role,
  key ID, attestation digest, time, constitution digest}. Event ID = blob hash.
  The stale case (master moved) is rejected; the watcher re-attests.
- **While stopped.** Merges are rejected (`line_stopped`) unless the candidate
  commit message carries the trailer `Andon-Event: <event id>` matching the
  current event. All other merge checks still apply.
- **Clear.** `POST /andon/clear` with a signed *passing* attestation whose
  commit is the current master, is not the commit the event was pulled on (a
  replayed attestation of the failing commit cannot clear it; a flake needs one
  trivial tagged commit), and which covers all required checks. The guard
  deletes `refs/line/andon` (CAS on the event ID). Clearing is mechanical.
- **Card.** On pull, the Andon station creates a P0 bead naming the event,
  commit and failing check, labelled for the line. Any agent may respond; the
  Maker runner prefers it. While the line stays stopped, every watcher step
  makes sure the event has a live (open or in_progress) card; if a fix landed
  and closed its card but master still fails, a fresh card names the
  still-failing master.
- Any station's key may pull and clear (role `andon` is not required); the
  evidence is what counts (spec: "any agent may pull, provided it carries
  evidence").

## Stations

- **Andon station (scripted, no LLM).** A host-side loop: poll the line master;
  for each new tip, call `/attest` for that commit with the andon token; fail →
  `/andon/pull` + card; pass while stopped → `/andon/clear`. An LLM Andon (for
  triage) is deferred.
- **Maker station.** A host-side runner loop:
  1. Pick a ready bead with label `line` (P0 andon cards first). Claim it on the
     host (`bd update --claim`). Beads are not exposed in the container in M1.
  2. Start the station container (`--rm`), with the line remote mounted
     read-only, the station URL and token in its environment, and model
     credentials passed through from the host environment
     (`CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`).
  3. Inside, clone master and run `claude -p` with the bead text and the line
     protocol: implement, run `just ci`, commit with the trailer `Bead: <id>`
     (plus `Andon-Event:` for andon cards), then `sweatshop line merge`; on
     `base_moved`, fetch, rebase and retry, with a bounded retry count.
  4. The runner reads the outcome from the line (a commit carrying
     `Bead: <id>` among those added to master since the run started), closes
     the bead on success, and unclaims it with a note on failure.
  5. A bead gets at most `--max-attempts` runs (default 3, counted per runner
     process; engine failures and interruptions do not count). After the last
     it is labelled `line-gave-up` and no longer offered. While
     `refs/line/andon` exists, only andon cards are offered.
- **Station image** `sweatshop-station`: Debian, Go (go.mod's toolchain), just,
  git ≥ 2.45.1, uv/python, ripgrep, fd, Node + Claude Code, `sweatshop`. The
  same image is the constitution's check runner image, so `just ci` sees one
  toolchain.

## `sweatshop line init`

```
sweatshop line init --dir <line-dir> --source <repo> [--engine docker] [--image sweatshop-station:dev] [--port 7468]
```

It creates:

- `remote.git` — a bare clone of `<repo>`'s master, with guard hooks installed;
- `constitution.toml` — trust `attest`; runner `container` with the engine and
  image; required check `ci = "just ci"`; guard paths `justfile` and
  `station/**` gated to role gemba (acceptance paths: none yet in sweatshop);
  trusted keys bound to roles;
- `stations/<name>/` for `maker-1` (role maker) and `andon-1` (role andon) —
  `witness.key` (0600), `token` (0600), `station.toml` (role, agent ID);
- `line.toml` — port and station list.

It refuses a non-empty `<line-dir>`. The line dir lives outside any workspace.

## Rulings

| Ruling | Why | Cost if wrong |
|---|---|---|
| Witness moves to `sweatshop line serve`; agentshd's witness becomes dev-only | Separates trust domains; resolves qp5 without a docker-exec executor backend | Spec said "agentshd is the witness". Re-merging later is mechanical |
| HTTP + bearer token, not a unix socket | Unix sockets do not cross the macOS VM boundary | One more listener; bound to loopback only |
| Git bundle intake | Never reads agent repo config (xw8); works over HTTP | Bundle size; capped at 256 MB |
| Agent commands not witnessed | Trust rests on the clean-room check run (P4) | Weaker forensics until witnessing is added |
| Witness signs failing runs | Andon needs signed failure evidence | None for merge (guard rejects failing) |
| Andon station is scripted | M1 needs a stop/clear loop, not judgment | LLM triage is deferred |
| Beads claimed/closed by the host runner | Beads sync inside containers is out of M1 | The runner pulls on the agent's behalf; the agent still chooses how to work |
| Go module and build caches are shared named volumes for check runs (king, 2026-10-17); `line serve --cold-checks` ignores them | Baking modules into the image went stale (sweatshop-59s); the caches are an optimisation only, so an empty volume downloads and fills itself. A writable cache shared across agent-authored code is poisonable, so the cold run proves the line builds from nothing | A cold run downloads everything and runs under `checks.cold_timeout` (default `"2h"`) |
| Fix trailer `Andon-Event:` | Simple, verifiable from the commit | Commit-message convention; enforced by guard |
| `justfile`, `station/**` gated to gemba (R13) | The candidate defines its own check; a maker must not weaken it in the commit it is judged by | Check/image changes cannot go through the line in M1; tests stay agent-editable |
| Check runs hardened, bounded, time-limited, one per station (R14) | Agent-authored check code must not starve the host or mint signed failures | Constitution grows optional runner limits and `checks.timeout` |
| Maker attempt cap + andon-only while stopped (R15) | An unlandable bead must not burn model time forever | A flaky bead may need `bd label remove <id> line-gave-up` |
