#!/bin/sh
set -eu

if [ -z "${OPENCODE_API_KEY:-}" ]; then
  echo "OPENCODE_API_KEY is required; config.toml names it as env_key and Codex refuses" >&2
  echo "to start without it. It is sent to OpenCode Go as the bearer token." >&2
  exit 2
fi

# No `codex login` step: the model provider in config.toml reads env_key directly,
# so the OpenAI OAuth flow is not used.

# --sandbox danger-full-access disables Codex's inner bubblewrap sandbox, which
# cannot create the user namespace it needs inside an unprivileged container. This
# container is the isolation boundary: it mounts no host path. It does hold the real
# OpenCode Go key, so model-issued commands run with full access inside the
# container (see e2e/README.md).
#
# The validator requires an execute_tool span, so a model response which skips this
# harmless command causes the E2E test to fail rather than weakening it.
exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  codex exec --skip-git-repo-check --sandbox danger-full-access \
  --model "${E2E_CODEX_MODEL:-gpt-5.6-luna}" \
  "Use the shell tool exactly once to run 'printf codex-otel-e2e'. Then reply with only: done."
