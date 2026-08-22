#!/bin/sh
set -eu

if [ -z "${OPENCODE_API_KEY:-}" ]; then
  echo "OPENCODE_API_KEY (your OpenCode Go API key) is required" >&2
  exit 2
fi

git init -q .
exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  opencode run -m "oclaude/${E2E_OPENCODE_MODEL:-ox-alpha-free}" \
    "Use the bash tool exactly once to run 'printf opencode-otel-e2e'. Then reply with only: done."
