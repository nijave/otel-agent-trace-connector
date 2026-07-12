#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "ANTHROPIC_API_KEY is required; this test runs a real paid Claude Code session." >&2
  exit 2
fi

export E2E_CLAUDE_MODEL="${E2E_CLAUDE_MODEL:-haiku}"
compose_files=(-f compose.claude.yaml)
if [[ -n "${E2E_CA_BUNDLE:-}" ]]; then
  if [[ ! -f "${E2E_CA_BUNDLE}" || ! -r "${E2E_CA_BUNDLE}" ]]; then
    echo "E2E_CA_BUNDLE is not a readable file: ${E2E_CA_BUNDLE}" >&2
    exit 2
  fi
  compose_files+=(-f compose.claude.ca.yaml)
fi

export E2E_RUN_ID="${E2E_RUN_ID:-claude-otel-$(date +%s)-$$}"
mkdir -p .e2e-output
rm -f .e2e-output/raw-logs.json .e2e-output/raw-traces.json .e2e-output/canonical-traces.json

run_compose() {
  docker compose "${compose_files[@]}" "$@"
}

cleanup() {
  run_compose down --remove-orphans
}
trap cleanup EXIT

run_compose build
run_compose up --detach --wait collector
run_compose run --rm --no-deps claude
run_compose run --rm --no-deps validator
