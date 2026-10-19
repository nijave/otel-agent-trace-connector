#!/usr/bin/env bash
# Runs every unpaid check CI runs, in the same shape, so a push never reds CI.
# Requires: go, golangci-lint v2.13.1 (the version CI pins), shellcheck, jq,
# docker, goreleaser. Run it from anywhere; it operates on the repo root.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

step() { printf '\n== %s ==\n' "$1"; }

step "gofmt"
unformatted=$(git ls-files '*.go' | xargs gofmt -l)
if [ -n "$unformatted" ]; then
  printf '%s\n' "$unformatted"
  echo "gofmt found unformatted files" >&2
  exit 1
fi

step "shell syntax"
bash -n scripts/*.sh
sh -n e2e/*/run.sh

step "shellcheck"
shellcheck scripts/[a-z]*.sh e2e/*/run.sh

step "golangci-lint (v2.13.1, the version CI pins)"
golangci-lint run --timeout=5m
(cd connector/codingagentconnector && golangci-lint run --timeout=5m)

step "generated files up to date"
./scripts/generate.sh
stale=$(git status --porcelain -- connector/codingagentconnector/internal/metadata connector/codingagentconnector/internal/metadatatest)
if [ -n "$stale" ]; then
  printf '%s\n' "$stale"
  echo "mdatagen output differs from the committed files; commit the regeneration" >&2
  exit 1
fi

step "vet"
go vet ./...
(cd connector/codingagentconnector && go vet ./...)

step "test"
go test ./...
(cd connector/codingagentconnector && go test ./...)
# Compile the e2e-tagged validator test and confirm it skips without a run.
go test -tags=e2e ./e2e/validator/

step "race"
go test -race ./...
(cd connector/codingagentconnector && go test -race ./...)

step "collector build and config validation"
go run go.opentelemetry.io/collector/cmd/builder@v0.159.0 --config builder-config.yaml
AWS_REGION=us-east-1 \
OTEL_QUEUE_DIRECTORY=/tmp/otelcol-queue \
OTEL_QUEUE_MAX_SIZE_BYTES=10485760 \
OTEL_S3_BUCKET=validation-only \
  ./dist/otelcol-coding-agents validate --config collector-config.yaml
AWS_REGION=us-east-1 \
OTEL_QUEUE_DIRECTORY=/tmp/otelcol-queue \
OTEL_QUEUE_MAX_SIZE_BYTES=10485760 \
OTEL_S3_BUCKET=validation-only \
  ./dist/otelcol-coding-agents validate --config examples/otelcol-s3.yaml
CANONICAL_OUT=/tmp/otelcol-routing-validate.json \
  ./dist/otelcol-coding-agents validate --config e2e/routing/tier2.yaml
docker run --rm -v "$PWD/e2e/routing/gateway.yaml:/gateway.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.159.0 validate --config /gateway.yaml

step "compose configurations"
export E2E_RUN_ID=ci-validation OPENCODE_API_KEY=validation-only OPENROUTER_API_KEY=validation-only
# Every stack file must parse; the per-stack assertions below encode each
# stack's credential contract: each agent container receives exactly one
# credential (its in-container name), equal to the validation value, and none of
# the other stacks' in-container names.
for f in compose.e2e-*.yaml; do
  docker compose -f "$f" config --quiet
done
# The Codex agent holds the real OpenCode Go key: config.toml names it as env_key
# and Codex sends it as the bearer token to OpenCode Go.
docker compose -f compose.e2e-codex.yaml config --format json \
  | jq -e '.services.agent.environment.OPENCODE_API_KEY == "validation-only"
           and (.services.agent.environment | has("OPENROUTER_API_KEY") | not)'
# Claude Code authenticates with x-api-key (ANTHROPIC_API_KEY); ANTHROPIC_AUTH_TOKEN
# must stay absent because Go rejects a bearer token on /messages.
docker compose -f compose.e2e-claude.yaml config --format json \
  | jq -e '.services.agent.environment.ANTHROPIC_API_KEY == "validation-only"
           and .services.agent.environment.ANTHROPIC_BASE_URL == "https://opencode.ai/zen/go"
           and (.services.agent.environment | has("ANTHROPIC_AUTH_TOKEN") | not)'
# The OpenAI-adhoc and Strands stacks each receive the OpenRouter key as
# OPENAI_API_KEY. No other stack's credential may leak in.
for stack in openai strands; do
  docker compose -f "compose.e2e-$stack.yaml" config --format json \
    | jq -e '.services.agent.environment.OPENAI_API_KEY == "validation-only"
             and (.services.agent.environment | has("OPENCODE_API_KEY") | not)
             and (.services.agent.environment | has("COPILOT_PROVIDER_API_KEY") | not)
             and (.services.agent.environment | has("LLM_API_KEY") | not)'
done
# The OpenCode e2e stack receives exactly one credential: an OpenCode Go key
# as OPENCODE_API_KEY. No other stack's credential may leak in.
docker compose -f compose.e2e-opencode.yaml config --format json \
  | jq -e '.services.agent.environment.OPENCODE_API_KEY == "validation-only"
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("OPENROUTER_API_KEY") | not)'
# The Copilot e2e stack receives exactly one credential: the OpenRouter key
# as COPILOT_PROVIDER_API_KEY, alongside the provider/model/OTEL configuration.
# No other stack's credential may leak in.
docker compose -f compose.e2e-copilot.yaml config --format json \
  | jq -e '.services.agent.environment.COPILOT_PROVIDER_API_KEY == "validation-only"
           and (.services.agent.environment
                | [has("COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_BASE_URL",
                       "COPILOT_MODEL", "COPILOT_OTEL_ENABLED",
                       "OTEL_EXPORTER_OTLP_ENDPOINT",
                       "OTEL_EXPORTER_OTLP_PROTOCOL")]
                | all)
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("OPENCODE_API_KEY") | not)'
# The Pi e2e stack receives exactly one credential: the OpenCode Go key as
# OPENCODE_API_KEY, read by pi's built-in opencode-go provider. No other
# stack's credential may leak in.
docker compose -f compose.e2e-pi.yaml config --format json \
  | jq -e '.services.agent.environment.OPENCODE_API_KEY == "validation-only"
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("OPENROUTER_API_KEY") | not)
           and (.services.agent.environment | has("COPILOT_PROVIDER_API_KEY") | not)
           and (.services.agent.environment | has("LLM_API_KEY") | not)'
# The OpenHands e2e stack receives exactly one credential: the OpenRouter key as
# LLM_API_KEY. No other stack's credential may leak in.
docker compose -f compose.e2e-openhands.yaml config --format json \
  | jq -e '.services.agent.environment.LLM_API_KEY == "validation-only"
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("OPENCODE_API_KEY") | not)
           and (.services.agent.environment | has("COPILOT_PROVIDER_API_KEY") | not)'
# The routing e2e stack replays a committed fixture; it receives no
# credential at all. Every stack key must stay absent.
docker compose -f compose.e2e-routing.yaml config --format json \
  | jq -e '.services.agent.environment
           | (has("OPENAI_API_KEY") or has("OPENCODE_API_KEY")
              or has("OPENROUTER_API_KEY") or has("COPILOT_PROVIDER_API_KEY")
              or has("LLM_API_KEY"))
           | not'

step "container images"
docker build --tag otelcol-coding-agents:check .
# Every e2e directory with a Dockerfile gets built, so a new stack is covered
# automatically.
for d in e2e/*/; do
  if [ -f "${d}Dockerfile" ]; then
    docker build --tag "$(basename "$d")-e2e:check" "$d"
  fi
done

step "goreleaser"
goreleaser check

printf '\nALL CHECKS PASSED\n'
