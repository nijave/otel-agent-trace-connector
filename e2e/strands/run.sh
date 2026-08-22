#!/bin/sh
set -eu

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py
