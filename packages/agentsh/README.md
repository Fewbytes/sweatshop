# agentsh package

The first Sweatshop marketplace package. It provides the agentsh supervised
execution runtime through Claude Code MCP tools and a pi.dev extension.

## Claude Code

Add the Sweatshop marketplace, then install `agentsh`:

```text
/plugin marketplace add https://github.com/OWNER/sweatshop
/plugin install agentsh@sweatshop
```

## pi.dev

For local development:

```bash
pi install ./packages/agentsh
```

The package metadata declares the pi extension and skill. The `agentsh` and
`agentshd` binaries must be installed separately or available on `PATH`.

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
