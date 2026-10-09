#!/usr/bin/env bash
# Maker station entrypoint: clone the line's master into /work and let Claude
# Code work the card. The container is the sandbox, so permission prompts are
# skipped inside it; the agent reaches the line only via `sweatshop line`.
set -euo pipefail

: "${SWEATSHOP_PROMPT:?SWEATSHOP_PROMPT is not set}"
: "${SWEATSHOP_AGENT_ID:?SWEATSHOP_AGENT_ID is not set}"

# The mounted remote belongs to the host user, not to agent; trust it
# explicitly or git refuses to read it.
git config --global --add safe.directory /line/remote.git

git clone -q /line/remote.git /work && cd /work \
    && git config user.name "$SWEATSHOP_AGENT_ID" \
    && git config user.email "$SWEATSHOP_AGENT_ID@line.local"
exec claude -p "$SWEATSHOP_PROMPT" --dangerously-skip-permissions --output-format text
