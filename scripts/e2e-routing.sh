#!/usr/bin/env bash
set -euo pipefail

# Zero-API-cost e2e: replays the pinned codex fixture through a contrib
# gateway into two tier-2 replicas and asserts per-conversation affinity.
# No model credentials needed.

compose_files=(-f compose.e2e-routing.yaml)
run_compose() { docker compose "${compose_files[@]}" "$@"; }

export E2E_RUN_ID="${E2E_RUN_ID:-routing-otel-$(date +%s)-$$}"
export CONVERSATIONS="${CONVERSATIONS:-8}"
mkdir -p .e2e-output
rm -f .e2e-output/canonical-a.json .e2e-output/canonical-b.json
trap 'run_compose down --remove-orphans' EXIT
run_compose build
run_compose up --detach --wait collector-a collector-b gateway
run_compose run --rm --no-deps agent
CANONICAL_FILE_A="${PWD}/.e2e-output/canonical-a.json" \
CANONICAL_FILE_B="${PWD}/.e2e-output/canonical-b.json" \
  go test -tags=e2e -count=1 -run '^TestRoutingAffinity$' ./e2e/validator/
