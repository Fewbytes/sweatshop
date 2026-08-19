# agentsh package

The first Sweatshop marketplace package. It provides the agentsh supervised
execution runtime through Claude Code MCP tools and a pi.dev extension.

## Claude Code

Add the Sweatshop marketplace, then install `agentsh`:

```text
/plugin marketplace add https://github.com/Fewbytes/sweatshop
/plugin install agentsh@sweatshop
```

## pi.dev

For local development:

```bash
pi install ./packages/agentsh
```

The package metadata declares the pi extension and skill. The `agentsh` and
`agentshd` binaries must be installed separately or available on `PATH`.

A SessionStart hook checks for `agentshd` at the start of every session and
prints a visible, actionable message if it's missing or won't run — you
should never be left wondering why the agentsh tools didn't show up. Run
`agentsh doctor` at any time for the full diagnostic (binary versions,
socket, daemon reachability, database, platform capabilities).

## Supported platforms

| OS      | Arch  | Support                              |
| ------- | ----- | ------------------------------------- |
| Linux   | amd64 | Full (cgroup containment)             |
| Linux   | arm64 | Full (cgroup containment)             |
| macOS   | arm64 | Degraded (no cgroup containment)      |
| macOS   | amd64 | **Not supported** — see below         |
| Windows | any   | **Not supported**                     |

macOS/amd64 (Intel Macs) can't be built at all: the storage layer's
`github.com/tursodatabase/go-libsql` dependency requires CGO and only ships
a prebuilt static library for `linux_amd64`, `linux_arm64`, and
`darwin_arm64`. `agentshd` also refuses to start on an unsupported
OS/arch combination at runtime, with a clear message, rather than running
degraded or crashing obscurely.

## Installing a prebuilt release

Each tagged release (`vX.Y.Z`) publishes `agentsh_<tag>_<os>_<arch>.tar.gz`
archives (containing both binaries) plus a `.sha256` checksum, for each
platform in the table above, on the
[GitHub Releases page](https://github.com/Fewbytes/sweatshop/releases).

```bash
curl -LO https://github.com/Fewbytes/sweatshop/releases/download/<tag>/agentsh_<tag>_<os>_<arch>.tar.gz
tar -xzf agentsh_<tag>_<os>_<arch>.tar.gz
mv agentsh_<tag>_<os>_<arch>/{agentsh,agentshd} ~/.local/bin/
```

## Building and installing from source

If you have a Go toolchain (and, for macOS/arm64 or Linux, a C compiler —
`go-libsql` needs CGO), build from the repository root with
[`just`](https://github.com/casey/just):

```bash
just install               # builds and installs to ~/.local/bin
just install /some/dir     # or install to a specific directory
```

`just install` is equivalent to:

```bash
cd agentsh
go build -o ~/.local/bin/agentsh  ./cmd/agentsh
go build -o ~/.local/bin/agentshd ./cmd/agentshd
```

Either way, make sure the install directory is on `PATH` — `agentsh` looks up
`agentshd` there (or next to its own binary) to auto-start the daemon on
first use.

If you'd rather point `agentsh` at an `agentshd` binary that isn't on `PATH`
or next to it, set `AGENTSHD_PATH`:

```bash
AGENTSHD_PATH=/path/to/agentshd agentsh health
```

See the repository root `justfile` (`just --list`) for the full set of
build/test/lint recipes.

## A note if you deny the Bash tool

Some setups deny the built-in `Bash` tool on the assumption `agentsh`
replaces it entirely. If you do this, verify the `agentsh` MCP tools are
actually available first (or run `agentsh doctor`) — denying `Bash` before
confirming agentsh works leaves a session with **no** command execution at
all. The SessionStart hook already warns about exactly this combination
(project `.claude/settings.json` denying `Bash` while `agentshd` is
missing) when it can find `jq` and your project settings file — but treat
that as a backstop, not a substitute for checking yourself first.

## Configuration

agentsh runs with no configuration at all, storing history locally. Remote
Turso sync is configured through a file, read in this order:

1. `--config <path>`
2. `<workspace>/.agentsh/config.json`
3. `~/.config/agentsh/config.json`

```json
{
  "turso": {
    "url": "libsql://your-database.turso.io",
    "auth_token_file": "~/.config/agentsh/turso-token",
    "sync_interval_seconds": 60
  }
}
```

Use `auth_token` to inline the token instead, and `chmod 600` the file if you
do — agentsh warns when a file holding a credential is readable by anyone else.

The daemon deliberately takes **no** settings from environment variables. Its
environment is inherited by every command it supervises, so a token kept there
would be handed to arbitrary agent-run processes and recorded in invocation
history. `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` are ignored if set, with a
warning, and are withheld from supervised commands.

Future related Sweatshop plugins should follow this package layout and declare
both adapters where applicable.
