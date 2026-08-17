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

Future related Sweatshop plugins should follow this package layout and declare
both adapters where applicable.
