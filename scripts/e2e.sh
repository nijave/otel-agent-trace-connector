#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is required; this test runs a real paid Codex session." >&2
  exit 2
fi

export E2E_RUN_ID="${E2E_RUN_ID:-codex-otel-$(date +%s)-$$}"
mkdir -p .e2e-output
rm -f .e2e-output/raw-logs.json .e2e-output/canonical-traces.json

docker compose up --build --abort-on-container-exit --exit-code-from validator validator
