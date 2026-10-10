#!/usr/bin/env bash
# Maker station entrypoint: clone the line's master into /work and let Claude
# Code work the card. The container is the sandbox, so everything inside it is
# allowed; the agent reaches the line only via `sweatshop line`.
#
# The station owns the agent's Claude settings (/etc/sweatshop, baked into the
# image). Project and local settings come from the clone, which the agent can
# edit and which are written for host sessions (the repo denies the built-in
# Bash in favour of agentsh), so they are not loaded.
set -euo pipefail

: "${SWEATSHOP_PROMPT:?SWEATSHOP_PROMPT is not set}"
: "${SWEATSHOP_AGENT_ID:?SWEATSHOP_AGENT_ID is not set}"

# The mounted remote belongs to the host user, not to agent; trust it
# explicitly or git refuses to read it.
git config --global --add safe.directory /line/remote.git

# Separate statements: set -e ignores failures inside an && chain except the last.
git clone -q /line/remote.git /work
cd /work
git config user.name "$SWEATSHOP_AGENT_ID"
git config user.email "$SWEATSHOP_AGENT_ID@line.local"
exec claude -p "$SWEATSHOP_PROMPT" \
    --setting-sources user \
    --settings /etc/sweatshop/claude-settings.json \
    --strict-mcp-config \
    --dangerously-skip-permissions \
    --output-format text
