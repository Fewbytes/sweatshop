# agentsh package

The first Sweatshop marketplace package. It provides the agentsh supervised
execution runtime through Claude Code MCP tools and a pi.dev extension. Both
adapters are thin wrappers around the same two binaries, `agentsh` (client)
and `agentshd` (daemon) — the plugin/extension install step below only
brings in the MCP wiring and skill files, **not** the binaries. You need
both steps.

## Quick start: Claude Code

1. Add the marketplace and install the plugin:

   ```text
   /plugin marketplace add https://github.com/Fewbytes/sweatshop
   /plugin install agentsh@sweatshop
   ```

2. Install the `agentsh`/`agentshd` binaries — no Go toolchain needed:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/Fewbytes/sweatshop/master/scripts/install-agentsh.sh | bash
   ```

   Make sure the install directory (`~/.local/bin` by default) is on `PATH`,
   then start (or restart) a Claude Code session. A SessionStart hook checks
   for `agentshd` on every session start and prints a visible, actionable
   message if it's missing or won't run — you should never be left wondering
   why the agentsh tools didn't show up. Run `agentsh doctor` at any time for
   the full diagnostic (binary versions, socket, daemon reachability,
   database, platform capabilities).

   Prefer to build from source instead? See
   [Building and installing from source](#building-and-installing-from-source)
   below.

## Quick start: pi.dev

1. Install the extension (for local development, from a checkout of this repo):

   ```bash
   pi install ./packages/agentsh
   ```

2. Install the `agentsh`/`agentshd` binaries, same as above:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/Fewbytes/sweatshop/master/scripts/install-agentsh.sh | bash
   ```

   The package metadata declares the pi extension and skill; pi.dev has no
   SessionStart-style bootstrap hook, so run `agentsh doctor` yourself after
   installing to confirm the daemon is reachable.

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
[GitHub Releases page](https://github.com/Fewbytes/sweatshop/releases). No Go
toolchain needed.

```bash
curl -fsSL https://raw.githubusercontent.com/Fewbytes/sweatshop/master/scripts/install-agentsh.sh | bash
```

`scripts/install-agentsh.sh` detects your OS/arch, downloads the matching
archive from the latest release (or `AGENTSH_VERSION=vX.Y.Z` for a specific
one), verifies its checksum, and installs both binaries to
`AGENTSH_INSTALL_DIR` (default `~/.local/bin`). From a checkout of the repo,
`just install-release [dest]` does the same thing. Or by hand:

```bash
curl -LO https://github.com/Fewbytes/sweatshop/releases/download/<tag>/agentsh_<tag>_<os>_<arch>.tar.gz
tar -xzf agentsh_<tag>_<os>_<arch>.tar.gz
mv agentsh_<tag>_<os>_<arch>/{agentsh,agentshd} ~/.local/bin/
```

## Building and installing from source

Requires a Go toolchain and a C compiler (`go-libsql` needs CGO — this is
just a native build, so it's on by default on any machine with a compiler
installed).

No checkout needed:

```bash
go install github.com/Fewbytes/sweatshop/agentsh/cmd/agentsh@latest
go install github.com/Fewbytes/sweatshop/agentsh/cmd/agentshd@latest
```

Or, from a checkout of the repository, with
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
