#!/bin/sh
set -eu

if [ -z "${COPILOT_PROVIDER_API_KEY:-}" ]; then
  echo "COPILOT_PROVIDER_API_KEY is required" >&2
  exit 2
fi

git init -q .
# Scoped --allow-tool does not grant permission in -p mode (headless has no
# user to approve); --allow-all-tools is the documented automatic approval.
# The container is disposable and the prompt asks for exactly one command.
exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  copilot -p "Use the shell tool exactly once to run 'printf copilot-otel-e2e'. Then reply with only: done." \
    --allow-all-tools
