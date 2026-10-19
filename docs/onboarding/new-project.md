# Onboarding a new project onto a line

Audience: an agent or human preparing a project to run on a sweatshop line.
The goal is a container image in which the project's `just ci` (or whatever the
required check is) passes, used both by the Maker station and by the witness's
clean-room check run.

## One container per station

A station runs **one container**. The runner's `Spec`
(`line/runner/runner.go`) describes a single image with mounts, env and
resource limits; the docker/podman backend (`line/runner/cli.go`) issues one
`run -d` per station or check. There is no network, pod or sidecar field.

Multi-container feasibility (investigated 2026-10-19, from the code, not a
live run):

- **Kubernetes:** a pod with sidecars (database, browser) fits naturally, but
  there is no k8s `Runner` backend yet. It would be a new backend, not an
  image change.
- **Podman:** pods (`podman pod create`) could share a network namespace across
  containers, but `Spec` has nowhere to express them, and the maker drives
  podman through the docker-compatible API/remote client. Needs `Runner`
  changes; not attempted.
- **Docker:** a user-defined network plus several `run` calls works, with the
  same missing `Spec` support.

Consequence: today a project that needs a compiler, language runtime or
service binary must **bake it into one custom image** built on the base below.
Services the checks need (databases, etc.) run as processes inside that
container.

## The base image

`station/Dockerfile` (final stage; `just station-image` builds
`sweatshop-station:dev`) is the base. It provides:

- the `sweatshop` CLI, `claude`, and `station-entrypoint` (clones the line
  remote into `/work`; inherited, do not override `CMD`);
- git (>= 2.45.1), `just`, `uv`, Node, Go, `gopls`, build-essential, python3,
  ripgrep, fd, ast-grep, ctags, jq, shellcheck, `tini` as PID 1;
- an unprivileged `agent` user (uid 1000), working directory `/work`.

Projects do not edit `station/Dockerfile` (that is this repository's own
image, and `station/**` is gated to gemba). They build their own image `FROM`
the base and add only their toolchain.

## Steps

1. Build the base: `just station-image`.
2. Write the project's `Dockerfile` from
   [`example-project/Dockerfile`](example-project/Dockerfile): take
   `ARG BASE_IMAGE`, switch to `USER root` to install, pin versions and verify
   downloads by checksum as the base does, finish with `USER agent`.
   Do not change `ENTRYPOINT` or `CMD`: the Maker runner passes no arguments and
   the check runner runs `<image> bash -c <check>`.
3. Build it: `docker build -f <Dockerfile> -t myproject-station:dev .`
4. Verify as the witness will: `docker run --rm myproject-station:dev bash -c
   'git --version && just --version && sweatshop --help >/dev/null && <your
   toolchain> --version'`, then run the project's check inside it.
5. Point the line at it: `sweatshop line init --dir ~/line --source <repo>
   --image myproject-station:dev`, or change `[runner] image` in the line's
   constitution.

## What the witness needs

- The required check (`[[checks.required]]`, default `just ci`) must pass in
  the image from a clean clone with no network assumptions beyond package
  registries; image contents are the check's whole toolchain.
- Language caches belong in engine volumes, not the image. The line mounts
  `sweatshop-go-mod-cache` and `sweatshop-go-build-cache` at `/cache/go-mod`
  and `/cache/go-build` (`GOMODCACHE`, `GOCACHE`). Caches for other toolchains
  are not mounted yet; a cold cache costs time against `checks.timeout`
  (default `30m`), so account for it or ask for a volume.
- Container ceilings come from the constitution's `[runner]` `memory` (4g),
  `pids` (1024) and `cpus` (2); size toolchain builds to fit.
- The container runs with all capabilities dropped and `no-new-privileges`;
  tooling that needs root at runtime will not work.
- Gate `justfile`, the project Dockerfile and image-related paths to a trusted
  role (`init` does this for `justfile` and `station/**`) so a maker cannot
  weaken the check in the commit it is judged by.
