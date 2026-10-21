# Line controller

Epic: `sweatshop-75ch`. Native engine clients: `sweatshop-7j6i`.

Stations need to run things on the line: a maker starts its check container, a
gemba worker brings up a staging or demo build, the viceroy asks for a release
to be deployed, heijunka may scale workers. Today the way to do that is to hand
the station the container engine. The maker, kaizen and gemba units mount the
host's podman socket, which is root on the host for any code an agent runs.
Kubernetes has the same problem in another form: a service account with
privileges per station.

This document proposes one privileged component per line, the controller. A
station asks for a workload; the controller, and nothing else, touches the
engine.

## Decision

- **A dedicated, deterministic controller**, `sweatshop line controller`, grown
  from `line deployer` (`line/deploy`, `deploy.Actuator`). No LLM in it.
- **`line serve` keeps authentication, policy and records** and gives up the
  engine socket. It already knows every station's identity and role, holds the
  constitution and runs the king's approval on the channel.
- **Heijunka and the viceroy are clients**, like makers and gemba. Heijunka
  mixes a flow-leveling job with root. The viceroy is an LLM agent that reads
  channel messages and cards, the component least fit to hold privilege; it is
  also itself a requester (succession, deploys).
- **One controller per line**, not per host. A host-wide controller would pool
  every line's privileges; per line matches the Kubernetes namespace and the
  per-line network and slice from `design-docs/multi-line-host.md`.

## Request lifecycle

```
station ──POST /v1/workloads──▶ serve ──policy──▶ approved? ──▶ record
                                                     │ no: ask the king on the channel
controller ──poll /v1/controller/pending──▶ serve
controller ──create/stop/remove──▶ engine (podman socket or Kubernetes)
controller ──POST /v1/controller/status──▶ serve ◀──GET /v1/workloads/{id}── station
```

States: `pending` → `approved` (or `denied`) → `creating` → `running` →
`exited` or `failed`; `expired` when the TTL passes; `removed` once the engine
object is gone.

### What a request carries

| Field | Meaning |
|---|---|
| `kind` | `test`, `staging`, `demo`, `build`; policy is per kind |
| `image` | a digest-pinned reference, or `build: {commit, dockerfile}` built from a commit on the line remote |
| `args`, `env` | not secret; secrets are never in the request |
| `secrets` | names from the allowed set; the controller resolves values from the line's secret source |
| `volumes` | names from the allowed set (for example `line-remote`, read-only); never host paths |
| `resources` | memory, cpus, pids, bounded by policy |
| `ttl` | bounded by policy; every workload expires |
| `expose` | optional `{port, name}`; the controller allocates the host port or route |
| `purpose` | free text for the audit and for the king |

The station's name and role come from its token, never from the body.

## Policy

A `[workloads]` section in the line's constitution (a sketch; the schema is
the first card):

```toml
[[workloads.rules]]
roles = ["maker", "gemba"]
kinds = ["test"]
approval = "auto"
max_per_station = 4
memory = "4g"
cpus = "2"
pids = 1024
ttl = "2h"

[[workloads.rules]]
roles = ["gemba"]
kinds = ["staging", "demo"]
approval = "king"
expose = true
ttl = "72h"

[workloads]
images = ["localhost/*", "registry.example/sweatshop/*"]
```

- Nothing outside a rule is allowed; the default is deny.
- `auto` approves a request inside its rule and quotas. `king` posts it to the
  channel through the same approval flow as deploy requests
  (`line/station/deploy.go`). A request outside every rule is denied, not
  escalated.
- Hard limits no rule can lift: no privileged containers, no host mounts (named
  volumes only), `cap-drop ALL`, `no-new-privileges`, non-root user, the line's
  own network, no host PID or network namespace.
- A station can list, read the logs of and stop its own workloads. The king
  and gemba see all of them.
- Every request, decision and state change is recorded with the station and
  rule that allowed it.

## The controller

- **Reconcile loop.** Desired state is the approved, unexpired requests in
  serve. Actual state is the engine's objects carrying the labels
  `sweatshop.line=NAME` and `sweatshop.workload=ID`. The controller creates
  what is missing, removes what has expired or been stopped, and removes
  labelled objects that have no request after a grace period.
- **Stateless.** It keeps nothing of its own, so a restart is safe. If it is
  down, nothing new starts and running workloads continue.
- **Backends** behind one interface, built on the engines' Go clients
  (`sweatshop-7j6i`): podman through its API socket, Kubernetes
  through a service account scoped to the line namespace (create, delete, get,
  list and logs on Pods and Jobs, nothing else).
- **Builds** are privileged too and are a request kind, built from a commit on
  the line remote as `EngineReleaseBuilder` does.

## The runner seam

`runner.Runner` already names no host path (mounts are named volumes, secrets
are values). A remote implementation posts a `Spec` to serve as a workload
request and waits on its state, so a maker that runs its check container through
`runner.Runner` needs no socket and no other change. The same implementation
later carries the witness's check runs and the viceroy's generation launches,
which today run on serve's own engine.

## Phases

1. Constitution schema, request API and records in serve, with policy and
   approval. No behaviour change yet.
2. The controller with a podman backend; `test` workloads.
3. The remote runner; makers, kaizen and gemba use it.
4. Remove the socket from the maker, kaizen and gemba units and from the
   `maker-service` recipe.
5. Move the witness's check runs and the viceroy's launches behind the
   controller, so serve holds no socket either.
6. The Kubernetes backend.

Until phase 5, serve stays privileged. Until phase 4, makers can still reach the
socket; the work is not done until phase 4 lands.

## Not in this design

- Scheduling across hosts. A controller manages one host's engine or one
  cluster namespace.
- A general container platform. The kinds are the ones the line has a use for.
- Replacing the king's approval for deploys of the line itself; `line deployer`
  keeps that flow and becomes one request kind of the controller.

## Open questions

- Where exposed ports and names go: a proxy the controller owns, or ports from
  a reserved range.
- Whether `staging` workloads need persistent volumes, and who may create
  them.
- Whether the controller signs its status reports with a witness key, as the
  witness does, so serve can trust them without trusting the transport.

## Cards

| Card | Piece |
|---|---|
| `sweatshop-7j6i` | Native Go clients for podman and Kubernetes, replacing CLI exec and hand-rolled HTTP |
| `sweatshop-75ch.1` | Constitution `[workloads]` schema and validation |
| `sweatshop-75ch.2` | Workload request API in serve: store, authz, policy, approval |
| `sweatshop-75ch.3` | `line controller` with a podman backend (needs `sweatshop-7j6i`) |
| `sweatshop-75ch.4` | Remote runner |
| `sweatshop-75ch.5` | Remove the podman socket from maker, kaizen and gemba units |
| `sweatshop-75ch.6` | Move witness check runs and viceroy launches behind the controller |
| `sweatshop-75ch.7` | Kubernetes backend (needs `sweatshop-7j6i`) |
