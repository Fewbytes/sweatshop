# Sweatshop marketplace

Sweatshop is a monorepo marketplace for related tools and plugins that help
LLM agents operate a software factory. **Claude Code is the primary target;
pi.dev is the secondary target.** `agentsh` is the first package, not the whole
product.

## Package contract

Every package under `packages/<name>/` should contain:

```text
<package>/
├── .claude-plugin/plugin.json  # Claude Code identity and metadata
├── package.json                # package identity and pi.dev resources
├── pi/extensions/              # pi.dev adapters
├── skills/                     # shared/proactive agent skills
├── mcp/                        # MCP configuration, when applicable
└── README.md
```

A package may depend on shared runtimes in the repository root or on another
marketplace package. Keep host-specific adapters thin and preserve one shared
behavior/tool contract.

## Claude Code installation

From GitHub:

```text
/plugin marketplace add https://github.com/OWNER/sweatshop
/plugin install agentsh@sweatshop
```

During local development:

```text
/plugin marketplace add /absolute/path/to/sweatshop
/plugin install agentsh@sweatshop
```

The root `.claude-plugin/marketplace.json` is the catalog. Each catalog entry
must point to a package containing `.claude-plugin/plugin.json`.

## pi.dev installation

Install a package from the repository checkout:

```bash
pi install ./packages/agentsh
```

A published package may be installed from npm or Git once its package is made
public. Pi package metadata declares extensions and skills; runtime binaries
remain separate dependencies and should be installed via releases, Homebrew,
or `go install`.

## Validation and releases

Validate the catalog and package manifests with:

```bash
npm run validate:marketplace
```

All packages share the release version. Release automation should build
platform-specific runtime binaries, generate checksums, publish package assets,
and update marketplace/npm metadata from the release tag. New packages must be
added to the marketplace catalog and have installation smoke tests before
release.
