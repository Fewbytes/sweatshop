# Sweatshop

**LLM Agent Plugins and Tools for Creating Software Factories**

Sweatshop is a collection of high-performance tools, plugins, execution runtimes, and agent skills designed to build autonomous software factories.

## Targets & Ecosystem Compatibility

1. **Claude Code (Primary)**:
   - MCP servers & tools
   - Hook integrations & workflow definitions
   - Skill definitions & custom commands
   - Drop-in shell runtime replacement (`agentsh`)

2. **pi.dev (Secondary)**:
   - Custom tools & extensions (`.pi/` ecosystem)
   - Skills & prompt templates
   - SDK integrations & custom providers

---

## Core Components

### 1. `agentsh` — Execution Runtime for LLM Agents
A high-performance Go-based daemon (`agentshd`), client CLI (`agentsh`), and MCP server providing isolated, supervised, out-of-context command execution, full command history, process trees, and stream inspection.
*See [`design-docs/agentsh-spec.md`](design-docs/agentsh-spec.md)*.

### 2. Issue Tracking & Workflow (`beads` / `bd`)
Embedded issue tracking with Dolt backend, providing branch-safe, cross-agent task assignment, atomic claims, and structured session close protocols.

### 3. `line` — Assembly Line Primitives
Witness attestations (DSSE/in-toto, Ed25519), the constitution (line rules), and the merge guard that fast-forwards the guarded branch only for trusted, passing attestations. agentsh embeds the witness; `sweatshop` administers keys and the guard's repository hook.

**Limitation:** the witness only protects its key and state from agents that run sandboxed apart from it. agentsh v1 runs the agent and the witness as the same host user (trust level `none`), so a malicious agent can read the witness key and alter witness state; higher trust levels are refused.

*See [`design-docs/assembly-line-spec.md`](design-docs/assembly-line-spec.md)*.

### 4. Agent Skills & Extensions
- **Claude Code**: Agent skills under `.agents/skills/` and `.claude/`.
- **pi.dev**: Extensions and skills compatible with pi agent harness.

---

## Directory Structure

```text
sweatshop/
├── agentsh/           # Go implementation of agentsh daemon, client, and MCP server
├── line/              # Assembly line: witness attestations, constitution, merge guard
├── design-docs/       # Technical specs (agentsh, factory architecture)
├── skills/            # Shared agent skills and tool definitions
├── .claude/           # Claude Code configuration and settings
├── .agents/           # Agent skills (Beads, etc.)
├── AGENTS.md          # Multi-agent system context and rules
└── CLAUDE.md          # Claude Code instructions and quick references
```

---

## Getting Started

1. **Issue Tracking**: Run `bd ready` or `bd show` to inspect work items.
2. **Claude Code**: Ready out-of-the-box with `.claude/settings.json` and `CLAUDE.md`.
3. **pi.dev**: Load skills and extensions via standard pi directory layout or package configuration.
4. **Build agentsh**: `just build` (or `just install` to put it on `PATH`) — see the root `justfile` (`just --list`) and [`packages/agentsh/README.md`](packages/agentsh/README.md).
