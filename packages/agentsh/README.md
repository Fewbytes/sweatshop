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
`agentshd` binaries must be installed separately or available on `PATH` —
there is no bundled binary distribution yet (tracked in sweatshop-u7z).

## Building and installing the binaries

From the repository root, with [`just`](https://github.com/casey/just) and Go
installed:

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
