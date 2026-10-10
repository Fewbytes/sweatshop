# agentsh executors — where agent commands run

Status: direction agreed (2026-10-10); no implementation plan yet. Parent spec:
`assembly-line-spec.md`; principles: `assembly-line-principles.md`.

## Problem

agentshd does two jobs: it **records** commands (history, output blobs,
replay) and it **executes** them. Today both happen in one process. That
process is the first MCP client's process on the host, and the station
containers do not run agentsh at all. The options were agentshd inside the
agent's container, as a sidecar, on the host, or many daemons with the agent
choosing among them. The three decisions below settle the shape.

## Decisions

1. **Commands produce event attestations.** Every command execution is an
   attestation-shaped record: an in-toto Statement whose predicate type is a
   command event. It carries the input digests, the command, the capability
   set, the exit code, output blob digests, and timing. It is unsigned for now,
   and signing comes later with executor keys bound in the constitution, like
   witness keys. Event attestations are a separate kind from the clean-room
   check attestations the witness already signs, but they are handled the same
   way: same envelope, same store, same verification path. Clean-room
   attestation keeps the higher implementation priority.
2. **Executors are chosen by capability (P1).** An executor *promises*
   capabilities: an OS and architecture, toolchains, a GPU, a warm cache,
   network reach, a trust level. An agent *requests* a capability set, and the
   line matches the request to an executor that promises it. Agents do not
   name hosts. The constitution says which roles may request which
   capabilities.
3. **No shared mutable state.** No shared volume between agent and executor.
   A command is a function of content-addressed inputs:

   ```
   (input tree digest, command, env, capability set)
       → (exit code, output blob digests, result tree digest)
   ```

   - **Input.** The agent's workspace is snapshotted to a tree, including
     uncommitted edits (written with a temporary index, so the working copy is
     not touched). The executor fetches only the objects it does not already
     hold, by hash. Git objects and bundles are the transport, the same as
     candidate intake.
   - **Output.** The result tree comes back as a diff against the input tree,
     and the agent applies it to its workspace. If the workspace moved during
     the command (concurrent edits), applying the diff is a three-way merge,
     and a conflict is reported, never resolved silently.
   - **What this buys.** Replay is exact. Results can be cached by input
     digest. The attestation subject is the input tree, so an event and a
     clean-room check describe the same kind of object. Executors can live
     anywhere, and nothing they hold is shared.

## Consequences for placement

- The **execution plane** can be anywhere that promises the requested
  capabilities: inside the agent's own container (capability "local", the
  cheapest), a sidecar, a host pool, or a remote farm. The protocol is the same
  everywhere: HTTP or gRPC with per-agent tokens, plus object transfer.
- The **record plane** (event store, blob store, replay) lives outside the
  agent's reach, with the witness or alongside it. Executors report events to
  it, and the agent never writes records directly.
- The **MCP server** in the agent's harness becomes a thin client: it snapshots
  the workspace, sends the request, applies the result.
- The witness's clean-room check runner becomes an executor that promises
  "clean room, signed". Event and check attestations converge on one framework.

## Open

- Snapshot cost for large workspaces: an incremental index, and object
  caching keyed by hash at the executor.
- Interactive commands (stdin, PTY) and long-running services do not fit
  "pure function". They need a session form whose events still come out
  attested.
- Secrets that commands need: the request names a capability ("can push to
  X"), and the executor holds the credential (the spec's held-credentials
  rule).
- Event attestation predicate schema; executor key binding in the
  constitution.
