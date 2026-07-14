#!/usr/bin/env bash
# Shared orchestration for the live e2e scripts. The sourcing script populates the
# `compose_files` and `support_services` arrays, then calls e2e_run.
# shellcheck disable=SC2154  # compose_files/support_services come from the sourcing script

run_compose() { docker compose "${compose_files[@]}" "$@"; }

# e2e_run builds the stack, waits for the long-running support services, runs the
# agent, then validates the exported traces on the host with the e2e-tagged Go
# tests (the support services stay up so the file exporter can flush while the test
# polls). $1 is the E2E_RUN_ID prefix (e.g. codex, claude). The agent runs with
# --no-deps because its dependencies are the support services already waited on.
e2e_run() {
  export E2E_RUN_ID="${E2E_RUN_ID:-$1-otel-$(date +%s)-$$}"
  mkdir -p .e2e-output
  rm -f .e2e-output/raw-logs.json .e2e-output/raw-traces.json .e2e-output/canonical-traces.json
  trap 'run_compose down --remove-orphans' EXIT
  run_compose build
  run_compose up --detach --wait "${support_services[@]}"
  run_compose run --rm --no-deps agent
  TRACE_FILE="${PWD}/.e2e-output/canonical-traces.json" \
    RAW_TRACE_FILE="${PWD}/.e2e-output/raw-traces.json" \
    go test -tags=e2e -count=1 ./e2e/validator/
}
