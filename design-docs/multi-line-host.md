# Multiple lines on one host

Epic: `sweatshop-mcfb`. Blocks the telemetry-nerd line.

Run N lines on one server. Each line has its own constitution, remote,
credentials, network and resource slice, and its own set of services: serve,
heijunka, andon, kaizen, makers, gemba, viceroy.

## Decisions

- **Containers under systemd Quadlet.** Every service is a Quadlet unit
  (`.container`, `.network`, `.volume`), templated by line name, so
  `sweatshop-serve@telemetry-nerd` and `sweatshop-serve@sweatshop` are the
  same template with different instances. Per-line port and project path come
  from the instance. Not doing kube YAML for podman.
- **State on a volume.** `line.db` and the local remote live on a per-line
  volume. Turso replaces `line.db` later. The remote is assumed to be an
  external git remote; tools use a dolt remote.
- **Isolation per line.** Each line gets its own podman network (stations reach
  their own line's serve only, no shared bridge between lines) and its own
  systemd slice with `CPUQuota` and `MemoryMax`.
- **Constitution is a per-line file.** `line init` seeds it. There is no
  inheritance between lines.
- **The runner interface stays the seam.** A k8s runner can follow later
  (ServiceAccount, Pods/Jobs, serve and heijunka as controllers) without
  changing callers. Quadlet is the first backend, not the abstraction.

## Per-line constitution

Each line owns `<line dir>/constitution.toml`. `line init` seeds it; after
that the file (and its `refs/line/constitution` copy once the owner commits
one) is that line's alone. A new line never reads another line's file, and
changing one line's constitution changes no other. To start a line like an
existing one, copy the file by hand and edit it.

`line init` writes these defaults (flags in parentheses):

- `[trust]` `level = "attest"`, plus one key per station (default stations
  `maker-1` as maker, `andon-1` as andon).
- `[runner]` `kind = "container"`, engine `docker` (`--engine`), image
  `sweatshop-station:dev` (`--image`).
- `[[checks.required]]` one check named `ci` running `just ci` (`--check`).
- `[[guards.paths]]` `justfile`, `station/**` and `acceptance/**`, each
  restricted to the gemba role, so a maker cannot weaken the check that
  judges its own commit.
- `[sovereign]` and `[channel]` only when the king's name, Discord ID and
  channel are given. Without them the line has no king, no delegations and
  no succession.

Per-line settings to review when a line is created:

- **`checks.required`**: the commands every candidate must pass before the
  guard merges it. Each entry has `name` and `command`; it runs in the
  runner's container. Differing lines (another language, another test
  command) differ here.
- **King** (`[sovereign]`): the one Discord user whose channel messages count
  as the king's. Approvals, proposals and epic decisions come only from this
  identity, on this line's `[channel]`.
- **Delegations** (`[[viceroy.delegations]]`): bounded powers the viceroy has
  without asking the king, to offer cards to the line (`offer = "line"`) or
  to close them (`close = true`), matched by card type, parent, label or ID.
  The default is none: every offer needs the king.
- **Approved epics**: not in the constitution. A delegation with
  `inherit = true` offers any non-epic card under an epic the king approved.
  The approvals are a ledger, `<line dir>/approved-epics.json`, written only
  by that line's `serve` when the king approves on the channel, so one line's
  approvals never apply to another.

## Viceroy image

By default each viceroy generation builds `station/Dockerfile` from the line
master. A project line does not have that file. Set `viceroy_image` in
`line.toml` (for example `viceroy_image = "localhost/sweatshop-station:dev"`)
and every generation runs that prebuilt image instead; nothing is built.
Lost: there is no per-commit viceroy build, so a merge never changes the
viceroy's own code, and succession restarts the same image.

## Work

| Card | Piece |
|---|---|
| `sweatshop-mcfb.1` | Slim `sweatshop-line` image (Go binary and git only) |
| `sweatshop-mcfb.2` | Quadlet templates and `sweatshop line install --name X`; replaces the hand-written units that were in `line/deploy/systemd` and `sweatshop-hostaddr.service`. Done: `line/quadlet` renders concrete per-line files (`sweatshop-NAME-serve.container`, ...) rather than `@` templates, because the port and project path come from `line.toml` and flags, not the instance name |
| `sweatshop-mcfb.3` | Per-line podman network and station reachability |
| `sweatshop-mcfb.4` | Secret source abstraction: file, env, systemd `LoadCredential`, later k8s Secret or Vault; removes `cat` in `ExecStart` |
| `sweatshop-mcfb.5` | Line-parameterised GitHub mirror (folds into `sweatshop-cmk`). Done: `sweatshop line mirror` pushes the constitution's storage mirrors fast-forward only with a per-line deploy key (`deploy_key`, `known_hosts` in the host directory given to `line install --mirror-creds`); that directory is mounted into the line's `-mirror` container only. The host-side `sweatshop-github-sync` timer is not in this repo; disable it once the mirror unit runs |
| `sweatshop-mcfb.6` | Document per-line constitution and `line init` defaults |
| `sweatshop-mcfb.7` | telemetry-nerd line: green CI and witness timeout |
