#!/bin/bash
# SessionStart bootstrap check for agentsh (sweatshop-2yn).
#
# Claude Code has no install/postinstall lifecycle hook, so this is the only
# place binary provisioning gets checked. Kept fast and read-only: it never
# builds or downloads anything itself, just detects the two failure modes
# that were previously silent (missing binary, binary present but the
# in-process --version invocation fails) and points at `agentsh doctor` for
# the full diagnostic instead of duplicating it here.
set -euo pipefail

locate_agentshd() {
	if [ -n "${AGENTSHD_PATH:-}" ]; then
		printf '%s' "$AGENTSHD_PATH"
		return 0
	fi
	command -v agentshd 2>/dev/null || true
}

binary="$(locate_agentshd)"

if [ -z "$binary" ] || [ ! -x "$binary" ]; then
	# This hook is read-only by design (see the file comment) — it reports,
	# it doesn't install. There are two install paths with different
	# tradeoffs (no-Go-toolchain-needed prebuilt vs. build-from-source), so
	# the choice belongs to the human, not a default this script picks.
	# ASSISTANT: ask the user (e.g. via AskUserQuestion) which of the two
	# options below they want before running either one automatically.
	cat <<'EOF'
[agentsh] agentshd binary not found on PATH (or via AGENTSHD_PATH).
  The agentsh MCP tools will not work until it is installed. Two ways to
  install — ask the user which they prefer before running one:

  1. Prebuilt release (no Go toolchain needed):
       curl -fsSL https://raw.githubusercontent.com/Fewbytes/sweatshop/master/scripts/install-agentsh.sh | bash
     or, from a checkout of the repo:
       just install-release

  2. Build from source (needs a Go toolchain and a C compiler — the
     storage layer uses CGO):
       just install               # from the sweatshop repo root
     or:
       go install github.com/Fewbytes/sweatshop/agentsh/cmd/agentsh@latest
       go install github.com/Fewbytes/sweatshop/agentsh/cmd/agentshd@latest

  Either way, ensure the install directory is on PATH afterward, or set
  AGENTSHD_PATH to point at the agentshd binary directly.
  Run `agentsh doctor` for a full diagnostic once installed.
EOF
	# Guardrail: a project that denies Bash on the assumption agentsh
	# replaces it, with no working agentsh, has no command execution at all.
	settings="${CLAUDE_PROJECT_DIR:-.}/.claude/settings.json"
	if [ -f "$settings" ] && command -v jq >/dev/null 2>&1; then
		if jq -e '(.permissions.deny // []) | index("Bash")' "$settings" >/dev/null 2>&1; then
			echo "[agentsh] WARNING: $settings denies Bash and agentsh is not installed — this session has no command execution at all until one of them is fixed."
		fi
	fi
	exit 0
fi

if ! "$binary" --version >/dev/null 2>&1; then
	echo "[agentsh] agentshd found at $binary but failed to run --version — it may be broken. Run \`agentsh doctor\` for details."
	exit 0
fi

exit 0
