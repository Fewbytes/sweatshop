# pi.dev agentsh integration

This project provides a pi extension at `.pi/extensions/agentsh.ts`.

## Install/use

From the repository:

```bash
pi -e ./.pi/extensions/agentsh.ts
```

For automatic discovery, place the extension in `~/.pi/agent/extensions/` or add
it to pi settings:

```json
{
  "extensions": ["/absolute/path/to/sweatshop/.pi/extensions/agentsh.ts"]
}
```

The extension registers `Bash`, `BashOutput`, and `BashInput`, forwarding typed
calls to the workspace-local `agentshd` Unix socket. The extension starts the
agentsh daemon lazily through the `agentsh health` client call.

The extension blocks pi's built-in `bash` tool so agents receive one clear shell
path. This is a host-level interception rather than removal of the built-in
implementation; user `!` shell commands remain pi host commands and should not
be treated as agent tool execution.

Requirements: pi.dev, Node.js, and built `agentsh`/`agentshd` on `PATH` (or set
`AGENTSH_PATH`).
