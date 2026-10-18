#!/usr/bin/env bash
# Station entrypoint. Maker mode: clone the line's master into /work and let Claude
# Code work the card. The container is the sandbox, so everything inside it is
# allowed; the agent reaches the line only via `sweatshop line`.
#
# The station owns the agent's Claude settings (/etc/sweatshop, baked into the
# image). Project and local settings come from the clone, which the agent can
# edit and which are written for host sessions (the repo denies the built-in
# Bash in favour of agentsh), so they are not loaded.
#
# SWEATSHOP_MODE=viceroy runs the viceroy's wake loop instead of one maker
# card (design-docs/m2-succession.md). The clone is then for reading only.
set -euo pipefail

: "${SWEATSHOP_AGENT_ID:?SWEATSHOP_AGENT_ID is not set}"

# The mounted remote belongs to the host user, not to agent; trust it
# explicitly or git refuses to read it.
git config --global --add safe.directory /line/remote.git

# Separate statements: set -e ignores failures inside an && chain except the last.
git clone -q /line/remote.git /work
cd /work
git config user.name "$SWEATSHOP_AGENT_ID"
git config user.email "$SWEATSHOP_AGENT_ID@line.local"

if [ "${SWEATSHOP_MODE:-maker}" = viceroy ]; then
    exec sweatshop viceroy run
fi

: "${SWEATSHOP_PROMPT:?SWEATSHOP_PROMPT is not set}"
exec claude -p "$SWEATSHOP_PROMPT" \
    --setting-sources user \
    --settings /etc/sweatshop/claude-settings.json \
    --plugin-dir /etc/sweatshop/plugin/sweatshop-tools \
    --strict-mcp-config \
    --dangerously-skip-permissions \
    --output-format text
