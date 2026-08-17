# agentsh — execution runtime for LLM agents

Spec for implementation by Claude Code. Read fully before writing code.

## 0. What this is

A replacement for Claude Code's built-in `Bash` tool. Not a new shell language —
bash stays the language. What changes is the runtime: a daemon that owns
sessions, supervises processes, stores output out-of-context, and returns
structured invocation records.

**Non-goals:** new syntax, object pipelines, autocomplete, readline, interactive
history, prompt customization, resource accounting, being a general-purpose
human shell.

## 1. Architecture

Three components:

- **`agentshd`** — per-workspace daemon. Owns everything stateful: sessions,
  SQLite history DB, blob store, process supervisor, interactivity watcher.
  ~95% of the code.
- **`agentsh`** — thin client binary. Unix socket RPC to the daemon. Subcommands
  map 1:1 to daemon ops. Exists for human debugging and as the MCP server's
  transport-free path.
- **MCP server** — the actual agent-facing surface. Wraps the daemon, exposes
  typed tools. Ships in the same binary (`agentshd mcp`).

Execution unit is `bash -c <command>` spawned fresh per invocation. No
persistent `bash -i`, no PTY, no shell-integration hooks, no LD_PRELOAD, no
bash fork.

**Language: Go.** Rationale: `os/exec` + `syscall` cover setsid/pgid/wait4
natively; goroutines fit N-concurrent-invocations-with-stream-pumps without
async coloring; trivial cross-compile to darwin/arm64 + linux/amd64. Use
`modernc.org/sqlite` (pure Go) to keep cgo off and the binary static.

Daemon starts lazily from the client if the socket is absent. No launchd or
systemd unit — agents run in containers that appear and vanish.

## 2. Tool surface (the critical design constraint)

The model's training prior is overwhelmingly bash. Do not fight it.

**Requirements:**

1. The built-in `Bash` tool MUST be disabled when agentsh is installed. A
   competing tool alongside it loses under context pressure.
2. The primary tool MUST be named `Bash`.
3. Its primary argument MUST be `command: string`, accepting a bash command
   line, so existing model behavior transfers unchanged.
4. No escape hatch. Inside the execution environment there is no other path to
   a shell. If the model can reach raw `bash`, it eventually will.

### Tools

| Tool | Args | Purpose |
|---|---|---|
| `Bash` | `command`, `session?`, `timeout?`, `background?`, `stdin?` | Run a command |
| `BashOutput` | `id`, `stream?`, `grep?`, `lines?`, `context?` | Retrieve/search stored output |
| `BashProcesses` | `session?` | List live invocations |
| `BashKill` | `id`, `signal?` | Terminate an invocation and its tree |
| `BashInput` | `id`, `data` | Write to a waiting process's stdin |
| `BashHistory` | `session?`, `cmd?`, `exit?`, `since?`, `limit?` | Query the history DB |
| `BashState` | `session?` | cwd, env delta, active jobs |
| `BashReplay` | `id` | Re-run a recorded invocation verbatim |

Only `Bash` and `BashOutput` need to be reliably remembered. Everything else is
reached via recovery hints (§5) or the skill file (§10).

## 3. Invocation record

Every invocation is a durable object. Committed to the DB **before** the
process starts, so a daemon crash leaves no orphan without a record.

```
Invocation {
  id            string      // "inv_" + 8 hex
  session       string
  argv          []string    // ["bash","-c",command]
  command       string      // raw, for replay and history search
  cwd           string
  env_delta     map[string]string
  stdin         *blob_ref
  state         enum        // running | exited | timeout | killed | waiting_on_input | daemon_lost
  exit_code     *int
  reason        enum        // ok | nonzero | timeout | killed | oom | not_found | permission | signal
  signal        *int
  started_at    time
  ended_at      *time
  duration_ms   int
  stdout        StreamRef
  stderr        StreamRef
  cwd_after     *string     // delta reported to caller
  env_after     map[string]string
  paths_touched []string    // §7, best-effort
}

StreamRef { sha256 string; bytes int; lines int; preview string; truncated bool }
```

`stdout` and `stderr` are **always separate**. Never merge them.

## 4. Output handling

- Streams are pumped to content-addressed files (`blobs/<sha256>`), uncapped.
  SQLite stores refs only.
- Default tool result returns exit code, reason, duration, and a bounded
  preview: first 100 lines + last 100 lines per stream, hard cap ~8KB per
  stream after that.
- **Silent truncation is forbidden.** Every elision states exactly what was
  dropped: byte count, line count, and which line ranges are shown.
- `BashOutput` supports: full retrieval, `--lines A:B`, `--grep RE` with
  `--context N`, and stream selection. Grep runs in the daemon over the blob;
  results are line-numbered so the model can page around a hit.
- Streams are readable while the invocation is still running.

## 5. Recovery affordances (highest-leverage feature after §2)

The model will not remember auxiliary tools from a system prompt at 100k
tokens. It *will* read the result in front of it. Therefore **every non-clean
terminal state MUST embed the literal next call.**

```
[stdout truncated — 4.2 MB, 61,204 lines; showing 1-100 and 61,105-61,204]
→ BashOutput(id="inv_7f3a2b1c", grep="error")
→ BashOutput(id="inv_7f3a2b1c", lines="8000:8200")
```

```
[waiting on input for 12s — process is reading stdin]
last output: Overwrite ./config.yaml? [y/N]
→ BashInput(id="inv_7f3a2b1c", data="y\n")
→ BashKill(id="inv_7f3a2b1c")
```

```
[timeout after 120s — process killed, tree reaped]
→ BashOutput(id="inv_7f3a2b1c")   # 340 lines captured before kill
→ Bash(command="...", timeout=600)
```

Same pattern for `not_found` (suggest similar binaries on PATH), `permission`,
and `oom`.

## 6. Process supervision

- Default timeout 120s, overridable per invocation, hard ceiling configurable.
  On expiry: SIGTERM, 5s grace, SIGKILL the whole tree.
- `background: true` returns immediately with the record; process keeps running;
  reachable via `BashProcesses` / `BashOutput` / `BashKill`.
- Containment is behind an interface with three impls. Nothing platform-specific
  leaks into the record schema.
  - **Linux — `Cgroup`**: one cgroup v2 per invocation. Kill the cgroup, whole
    tree dies. No PGID games, no orphan hunting. Detects OOM.
  - **macOS — `ProcessGroup`**: `setsid` per invocation + `killpg`. Watch exits
    with kqueue `EVFILT_PROC`/`NOTE_EXIT`. A child that calls `setsid` itself
    escapes the group — sweep descendants via libproc `proc_listchildpids` at
    kill time and again after the grace period. `setrlimit` for coarse caps.
  - **`Null`**: fallback, best-effort kill.
- Exit status and rusage come from `wait4` on both platforms.

### Interactivity detection

The worst bash-tool failure is blocking on a prompt until timeout. Detect it:
no output for N seconds (default 10) **and** the process is blocked reading
stdin. On Linux read `/proc/<pid>/wchan` + `/proc/<pid>/syscall`; on macOS use
libproc `PROC_PIDFDVNODEINFO` / kqueue heuristics. Fall back to the output-idle
heuristic alone where unavailable.

On detection: set state `waiting_on_input`, return immediately with the last 20
lines and the recovery hints from §5. Do **not** burn the timeout.

Default stdin is `/dev/null` unless `stdin` or interactive mode is requested —
prevention beats detection.

## 7. Sessions and state

- Sessions are explicit and named; default session per workspace.
- State is carried forward, not held in a live process. After each invocation,
  the wrapper emits `pwd`, `declare -p`, `declare -f`, `set -o`, `shopt` to a
  control fd (fd 3 — never stdout/stderr). Next invocation replays it as a
  prelude.
- `BashState` returns cwd, env delta from baseline, active jobs.
- Each invocation result reports its **delta**: cwd changed, these vars set.
  Agents lose track of `cd` constantly; make it visible.
- `paths_touched`: best-effort filesystem change summary scoped to the
  workspace (fanotify on Linux, FSEvents on macOS). Nice-to-have; ship behind a
  flag. Feeds a future undo journal.

## 8. Environment hygiene

Applied to every invocation, non-negotiable:

```
TERM=dumb NO_COLOR=1 CLICOLOR=0
PAGER=cat GIT_PAGER=cat LESS=FRX
DEBIAN_FRONTEND=noninteractive CI=1
PYTHONUNBUFFERED=1
```

Plus `set -o pipefail`, stdin from `/dev/null` by default, no `.bashrc` / no
`--login` (use `bash --noprofile --norc -c`).

## 9. Storage

- SQLite at `<workspace>/.agentsh/history.db`. WAL mode.
- Blobs at `<workspace>/.agentsh/blobs/<sha256>`, content-addressed, dedup free.
- Index on `(session, started_at)`, `(exit_code)`, and FTS on `command`.
- `BashHistory` must answer in one call: failed invocations since a timestamp,
  invocations matching a command glob, invocations by exit code.
- Retention: configurable blob GC by age and total size. Records outlive blobs;
  a GC'd blob returns a clear "output expired" rather than a missing file.
- On daemon restart: reconcile. Any `running` record whose pid is gone becomes
  `daemon_lost`.

## 10. Skill file

Ship `SKILL.md` covering the tools with no triggering failure — `BashHistory`,
`BashReplay`, `BashState`, background invocations. These are proactive uses that
recovery hints can't surface. Skill loading is relevance-gated, so it survives
context growth better than CLAUDE.md.

Do **not** put "prefer agentsh over bash" instructions anywhere. That approach
is known to fail; §2.1 makes it unnecessary.

## 11. Build order

1. Daemon skeleton + socket RPC + client. `Bash` with `bash -c`, three fds,
   default timeout, `ProcessGroup` containment. Record to SQLite, blobs to disk.
2. `BashOutput` with grep/lines. Truncation notices with recovery hints (§5).
   **Stop here and dogfood.** This is most of the value.
3. Interactivity detection.
4. Sessions + state carry-forward + deltas.
5. Cgroup containment on Linux.
6. `BashHistory` queries, `BashReplay`.
7. MCP server, built-in `Bash` disabled, `SKILL.md`.
8. Optional: `paths_touched`, undo journal, batched multi-step invocations.

## 12. Testing

- Fixture commands per failure class: prompt-blocker, 500MB stdout emitter,
  orphan spawner (`setsid` grandchild), fast-exit nonzero, OOM'er, signal
  suicide, slow trickle output.
- Assert: no orphan processes survive kill, stdout/stderr never interleave,
  truncation notices are always present when elision happens, daemon kill
  mid-invocation leaves a reconcilable record.
- End-to-end: run a real agent loop against a repo, diff context token usage vs
  the built-in bash tool. That number is the project's headline metric.
