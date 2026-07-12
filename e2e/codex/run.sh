#!/bin/sh
set -eu

if [ -n "${E2E_CODEX_MODEL:-}" ]; then
  exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
    codex exec --skip-git-repo-check --sandbox read-only --model "${E2E_CODEX_MODEL}" \
    "Use the shell tool exactly once to run 'printf codex-otel-e2e'. Then reply with only: done."
fi

# The validator requires an execute_tool span, so a model response which skips
# this harmless command causes the E2E test to fail rather than weakening it.
exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  codex exec --skip-git-repo-check --sandbox read-only \
  "Use the shell tool exactly once to run 'printf codex-otel-e2e'. Then reply with only: done."
