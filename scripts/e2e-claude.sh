#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${AWS_REGION:-}" ]]; then
  echo "AWS_REGION is required; this test runs a real paid Claude model on Bedrock." >&2
  exit 2
fi

export E2E_CLAUDE_MODEL="${E2E_CLAUDE_MODEL:-us.anthropic.claude-haiku-4-5-20251001-v1:0}"
compose_files=(-f compose.claude.yaml)
if [[ -n "${AWS_PROFILE:-}" ]]; then
  export E2E_AWS_CONFIG_DIR="${E2E_AWS_CONFIG_DIR:-${HOME}/.aws}"
  if [[ ! -d "${E2E_AWS_CONFIG_DIR}" || ! -r "${E2E_AWS_CONFIG_DIR}" ]]; then
    echo "E2E_AWS_CONFIG_DIR is not a readable directory: ${E2E_AWS_CONFIG_DIR}" >&2
    exit 2
  fi
  compose_files+=(-f compose.claude.aws-profile.yaml)
fi
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
