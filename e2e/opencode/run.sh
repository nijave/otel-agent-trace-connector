#!/bin/sh
set -eu

if [ -z "${OPENCODE_API_KEY:-}" ]; then
  echo "OPENCODE_API_KEY (your OpenCode Go API key) is required" >&2
  exit 2
fi

# opencode's built-in opencode-go provider reads OPENCODE_API_KEY and sends the
# session header OpenCode Go requires.
git init -q .
exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  opencode run -m "opencode-go/${E2E_OPENCODE_MODEL:-glm-5.3-flash}" \
    "Use the bash tool exactly once to run 'printf opencode-otel-e2e'. Then reply with only: done."
