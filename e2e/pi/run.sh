#!/bin/sh
set -eu

if [ -z "${OPENCODE_API_KEY:-}" ]; then
  echo "OPENCODE_API_KEY is required (OpenCode Go endpoint + key)" >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  pi --no-session \
    --model "${E2E_PI_MODEL:-opencode-go/claude-haiku-5-5}" \
    --tools bash \
    -p "Use the bash tool exactly once to run 'printf pi-otel-e2e'. Then reply with only: done."
