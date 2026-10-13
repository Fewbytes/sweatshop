# M2 — Succession

Milestone 2: a viceroy that lives on the line and can build and run its own
successor. Epic: `sweatshop-0mw.2` (under the Viceroy epic `sweatshop-0mw`).
Parent spec: `assembly-line-spec.md` ("Viceroy"); M1: `m1-line-live.md`.

The king's mandate (2026-10-10): the first viceroy (a Claude Code session on
the owner's host) builds its successor. The torch passes when the successor
meets the exit criteria below.

## Exit criteria

1. The viceroy runs as a line station: role `viceroy`, its own token, inside a
   container. It holds no credential except its station token and the model
   credential.
2. It talks to the king on Discord, in `#sweatshop-assembly-line`.
3. It opens cards for the king, shaped in conversation.
4. It reports line status (master, andon, landings, station activity) and card
   status on request.
5. It builds and runs its successor. It files cards that change the viceroy,
   the line lands them, the viceroy requests succession to the new master, and
   the line starts the new version, which checks in and takes over. One full
   succession completes with no help from a host session.

Out of scope: other humans as principals, non-Discord media, LLM triage of
andon events, viceroy changes to line infrastructure (`line serve` itself is
still deployed by the owner).

## Trust domains

```
 host (operator uid; owner-deployed code)            containers
 ┌───────────────────────────────────────────┐       ┌──────────────────────────┐
 │ sweatshop line serve                      │ HTTP  │ viceroy (gen N)          │
 │  ├ station API (+ cards, channel,         │◀──────│  sweatshop viceroy run   │
 │  │   testament, succession routes)        │ token │   └ claude -p per wake   │
 │  ├ channel: Discord REST poller (bot      │       └──────────────────────────┘
 │  │   token held here), king commands      │       ┌──────────────────────────┐
 │  ├ cards: bd on the host, role-gated      │◀──────│ maker, andon (M1)        │
 │  └ viceroy supervisor: build image at C,  │       └──────────────────────────┘
 │      start gen N+1, await check-in        │
 └───────────────────────────────────────────┘
```

- **Only the container part is the successor.** Everything that runs on the
  host (`line serve`, the channel poller, the cards API, the supervisor) is
  line infrastructure. The owner reviews and deploys it, exactly as in M1. The
  viceroy evolves its own container program: the wake loop, the prompt, and
  the client commands it uses. Succession never runs agent-authored code on
  the host, so a malicious or broken successor holds only a viceroy token.
- **The image is built in the engine.** The supervisor runs
  `docker build -f station/Dockerfile` on a checkout of commit C. The build
  runs in the engine's VM, not on the host. `station/**` and `justfile` stay
  gated to gemba (the owner), so the viceroy cannot change how its own image
  is assembled, only the code compiled into it.
- **Model credential.** As for makers, the supervisor passes
  `CLAUDE_CODE_OAUTH_TOKEN` from `model.cred` into the container environment.

## Principals and governance

The constitution grows three sections:

```toml
[sovereign]
name = "nukemberg"
discord = "477116424171814945"      # Discord user ID; the only king identity

[channel]
kind = "discord"
channel = "1558515236938588312"     # #sweatshop-assembly-line

[[viceroy.delegations]]             # cards the viceroy may offer without the king
offer = "line"
types = ["bug"]

[[viceroy.delegations]]
offer = "line"
parent = "sweatshop-0mw.2"          # this milestone's cards and their children

[[viceroy.delegations]]             # cards the viceroy may close without the king
close = true
ids = ["sweatshop-0mw.1.4"]         # or parent = "<card id>", types = [...], labels = [...]

[viceroy]
succession = true                   # may request its own succession
```

- **A king's decision** reaches the line only as a channel message whose
  author ID equals `sovereign.discord`. The checking is done by `line serve`,
  not by the viceroy. The grammar is mechanical: a message from the king
  matching `^approve (<card id>)$` labels that card `line` and comments with
  the Discord message ID. The viceroy may suggest the words to type; it can
  never type them.
- **Delegations** let `line serve` honour the viceroy's request to offer a
  card, if the card matches one of them. Otherwise the card is labelled
  `line-proposed` and waits for the king's `approve`.
- **Close delegations** let the viceroy request `POST /v1/cards/{id}/close`
  (with a reason) for cards in the grant's `ids`, under its `parent`, of one of its
  `types`, or carrying one of its `labels`. Types are safer than labels, which
  any agent can set. The guard also requires the card to carry a comment recording the decision.
  Adding or widening a grant is a constitution change, gated by the sovereign.
- **Everything else in the channel is data.** Messages from other humans are
  forwarded to the viceroy with `principal: "unknown"` and carry no authority.

## Station API additions (line serve)

Every route is role-gated. Maker and andon tokens get 403 on all write routes
below. All cards routes call `bd` on the host with fixed arguments and
validated IDs (`^[a-z0-9-]+(\.[0-9]+)*$`); text goes on stdin or in an
argument, never through a shell.

| Route | Roles | Effect |
|---|---|---|
| `GET /v1/cards?status=&label=&parent=` | all | `bd list --json`, filtered |
| `GET /v1/cards/{id}` | all | `bd show --json` |
| `POST /v1/cards` | viceroy | create: title, description, type, priority, parent, acceptance |
| `POST /v1/cards/{id}/comments` | viceroy | add a comment |
| `POST /v1/cards/{id}/close` | viceroy | close a card a close delegation covers and that has a comment; 403 if not delegated, 409 if no comment (makers close their own claim through the same route) |
| `POST /v1/cards/{id}/offer` | viceroy | label `line` if a delegation covers the card; else label `line-proposed`; the response says which |
| `GET /v1/channel/inbox?after=<msg id>&wait=<dur>` | viceroy | messages since `after` (long-poll, at most 60 s); each carries `id, author, author_id, principal (king|unknown), text, ts` |
| `POST /v1/channel/post` | viceroy | post text (≤ 2000 chars, split by the client) to the channel |
| `POST /v1/channel/typing` | viceroy | show the bot as typing; the viceroy loop renews it while a run is in progress |
| `GET/PUT /v1/testament` | viceroy | the viceroy's notes, ≤ 64 KiB, stored as a blob under `refs/line/testament/<role>` in the line remote, so every version is kept |
| `POST /v1/viceroy/succeed?commit=` | viceroy | request succession to C (see below) |
| `POST /v1/viceroy/checkin` | viceroy | the new generation reports it is up |

The channel poller is a goroutine in `line serve`. It calls
`GET /channels/{id}/messages?after=` every 3 s, appends to `channel/inbox.jsonl` in the line dir, and applies king
commands. It skips the bot's own messages. The bot token comes from
`discord.cred` (0600) in the line dir, fetched from Bitwarden item
`4d0f0a1d-b765-4307-b019-b4df01100e94`. A missing credential turns the
channel off and the routes return 503.

With `transport = "gateway"` in `[channel]` (default `"poll"`), `line serve`
also holds a Discord gateway websocket (identify, heartbeat, resume, reconnect
with backoff). The REST poller idles while the gateway is up and takes over when
it drops; every (re)connect backfills over REST after `last_id`, and delivery
dedupes by message ID. The gateway adds two approvals that go through the same
path as the text `approve <id>`: the sovereign's `/approve card:<id>` slash
command (guild command, autocomplete from `line-proposed` cards, ephemeral ack)
and the sovereign's ✅ reaction on a bot post that names exactly one card.
Interactions and reactions from anyone else are ignored. The inbox format and
the viceroy API are unchanged; posting stays on REST.

## The viceroy container

`sweatshop viceroy run` is the container's entrypoint (via a mode of
`station/entrypoint.sh`). It loops:

1. Wait for a wake: new inbox messages (long-poll), an andon change seen in
   `/v1/status`, or the heartbeat (default 6 h, a status summary to the king).
2. Build the prompt: the viceroy charter (role, authority, the untrusted-input
   rule), the testament, the last 30 channel messages, the new messages, and
   line status.
3. Run `claude -p` with station settings. Its tools are the `sweatshop line`
   client commands: `status`, `cards list|show|create|comment|offer`,
   `channel post`, `testament get|put`, `viceroy succeed`. It may clone master
   read-only to read code and docs. It never merges.
4. On exit, record the last handled message ID in the testament, so a crash
   reprocesses at most one batch.

One `claude -p` runs at a time. Messages that arrive during a run join the
next batch.

## Succession

```
gen N: cards for viceroy changes ─▶ line lands them on master (commit C)
gen N: POST /v1/viceroy/succeed?commit=C
serve: verify C == master, line not stopped, constitution allows succession,
       C differs from gen N's commit
serve: clean-room attest C with the viceroy witness (must pass)
serve: docker build image sweatshop-viceroy:<C> from C
serve: start gen N+1 (same station identity, env SWEATSHOP_VICEROY_GEN=N+1,
       SWEATSHOP_PREDECESSOR=<gen N commit>)
gen N+1: reads testament, posts a check-in to the channel, POST /checkin
serve: on check-in within 10 min, stop gen N; record viceroy/current = C
       on timeout or exit, kill gen N+1, keep gen N, post the failure
```

- Both generations hold the same token during the handover. Only one is
  active: until check-in, `serve` refuses `channel/post` and card writes from
  the container whose generation is not current, except the new
  generation's single check-in post. The supervisor tells each container its
  generation through the container name, and routes check the caller's
  `X-Sweatshop-Gen` header against the supervisor's record. This is a
  liveness guard, not a security boundary: both generations are equally
  trusted.
- `viceroy/current` and the history of generations live in the line dir
  (`viceroy/generations.jsonl`). On restart, `line serve` starts the current
  generation.
- **Bootstrap.** Generation 1 is started by the owner with
  `sweatshop line viceroy start --commit <C>`. The first viceroy (the host
  session) builds generation 1 through the line, starts it, and hands over.
  Generation 1 must then complete one succession on its own.

## Build plan (cards under `sweatshop-0mw.2`)

1. `sweatshop-0mw.1`: constitution role `viceroy`; sections `sovereign`,
   `channel` and `viceroy` (delegations, succession), validated.
2. `sweatshop line station add --name --role`: add a station (key, token,
   trust key) to an existing line.
3. Cards API and client (`sweatshop line cards …`).
4. Channel: Discord poller, inbox/post routes, king `approve` command, and the
   client (`sweatshop line channel …`).
5. Testament route and client.
6. The viceroy container: `sweatshop viceroy run`, charter prompt, entrypoint
   mode.
7. Supervisor and succession: `line viceroy start`, the `succeed` and
   `checkin` routes, image build, handover and rollback.

The owner does the following on the host: `station/**` changes (the
entrypoint mode), `discord.cred`, the launchd environment, and deploying
`line serve` after each landed infrastructure card.

## Rulings

| Ruling | Why | Cost if wrong |
|---|---|---|
| Successor = container program; host parts are owner-deployed | A successor must never run agent-authored code on the host | The viceroy cannot evolve line infrastructure on its own; those changes need an owner deploy |
| Discord REST polling in `line serve`, not the gateway | No dependency, no long-lived socket; the bot token stays on the host | ~3 s latency; rate limits bound the poll interval |
| King commands parsed by `line serve`, matched on author ID | The viceroy's word is never the king's (spec) | Text grammar; a compromised Discord account is a compromised king |
| Cards via host `bd` behind role-gated routes | Beads is not in containers (M1); routes keep the viceroy narrow | `line serve` shells out to bd; bd failures surface as 502 |
| Succession re-attests C clean-room | The line keeps no attestation store yet | One check run per succession |
| Testament as blobs under a line-remote ref | No mutable shared state; history comes free | 64 KiB cap |
