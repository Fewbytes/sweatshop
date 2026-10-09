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
  and untrusted. Its `line_attest`/`line_merge` ops remain as the trust-`none`
  dev path and are removed later. This resolves `sweatshop-qp5`: no agent
  command ever runs as the witness's uid.
- **Station API.** HTTP on `127.0.0.1:<port>`. Containers reach it through the
  engine's host alias (`host.containers.internal`; verified with podman
  machine). Each station has a bearer token; the token selects the station, and
  the station fixes role, agent ID and key. A stolen token is only that agent's
  own identity. Production Linux hosts may use a unix socket mount instead.
- **Candidate transfer is a git bundle** (`sweatshop-xw8`). The client bundles
  `HEAD ^<line master>` and POSTs it. The witness verifies the bundle against
  its cache (seeded from the line remote, which it trusts) and fetches the
  commit from the bundle file. The agent's repository and its config are never
  read.
- **Agent commands are not witnessed in M1.** Trust rests on the witness's own
  clean-room check run. Agent command logs are untrusted evidence. Host-side
  witnessing of agent commands is deferred.

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
  commit is the current master and which covers all required checks. The guard
  deletes `refs/line/andon` (CAS on the event ID). Clearing is mechanical.
- **Card.** On pull, the Andon station creates a P0 bead naming the event,
  commit and failing check, labelled for the line. Any agent may respond; the
  Maker runner prefers it.
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
  4. The runner reads the outcome from the line (a merged commit carrying
     `Bead: <id>`), closes the bead on success, and unclaims it with a note on
     failure.
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
  image; required check `ci = "just ci"`; acceptance guard paths (none yet in
  sweatshop); trusted keys bound to roles;
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
| No shared cache volume for check runs | A writable cache shared across agent-authored code is poisonable | Slower checks (module downloads per run) |
| Fix trailer `Andon-Event:` | Simple, verifiable from the commit | Commit-message convention; enforced by guard |
