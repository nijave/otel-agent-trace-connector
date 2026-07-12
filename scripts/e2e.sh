#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is required; this test runs a real paid Codex session." >&2
  exit 2
fi

export E2E_RUN_ID="${E2E_RUN_ID:-codex-otel-$(date +%s)-$$}"
mkdir -p .e2e-output
rm -f .e2e-output/raw-logs.json .e2e-output/raw-traces.json .e2e-output/canonical-traces.json

cleanup() {
  docker compose down --remove-orphans
}
trap cleanup EXIT

docker compose build
docker compose up --detach --wait collector
docker compose run --rm --no-deps codex
docker compose run --rm --no-deps validator
