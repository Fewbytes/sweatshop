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
