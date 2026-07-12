#!/bin/sh
set -eu

model_args=""
if [ -n "${E2E_CODEX_MODEL:-}" ]; then
  model_args="--model ${E2E_CODEX_MODEL}"
fi

# The validator requires an execute_tool span, so a model response which skips
# this harmless command causes the E2E test to fail rather than weakening it.
# shellcheck disable=SC2086
exec codex exec --skip-git-repo-check --sandbox read-only ${model_args} \
  "Use the shell tool exactly once to run 'printf codex-otel-e2e'. Then reply with only: done."
