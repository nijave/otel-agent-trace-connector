#!/bin/sh
set -eu

if [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ]; then
  echo "ANTHROPIC_AUTH_TOKEN is required (z.ai endpoint + key)" >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  pi --no-session \
    --model "${E2E_PI_MODEL:-zai/glm-4.7}" \
    --tools bash \
    -p "Use the bash tool exactly once to run 'printf pi-otel-e2e'. Then reply with only: done."
