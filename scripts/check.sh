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
bash -n scripts/e2e.sh scripts/e2e-claude.sh scripts/e2e-openai.sh scripts/e2e-strands.sh scripts/e2e-opencode.sh scripts/e2e-copilot.sh scripts/e2e-openhands.sh scripts/e2e-pi.sh scripts/generate.sh scripts/lib-e2e.sh
sh -n e2e/codex/run.sh e2e/claude/run.sh e2e/openai-adhoc/run.sh e2e/strands/run.sh e2e/opencode/run.sh e2e/pi/run.sh e2e/copilot/run.sh e2e/openhands/run.sh

step "shellcheck"
shellcheck scripts/e2e.sh scripts/e2e-claude.sh scripts/e2e-openai.sh scripts/e2e-strands.sh scripts/e2e-opencode.sh scripts/e2e-copilot.sh scripts/e2e-openhands.sh scripts/e2e-pi.sh scripts/generate.sh scripts/lib-e2e.sh e2e/codex/run.sh e2e/claude/run.sh e2e/openai-adhoc/run.sh e2e/strands/run.sh e2e/opencode/run.sh e2e/pi/run.sh e2e/copilot/run.sh e2e/openhands/run.sh

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
go run go.opentelemetry.io/collector/cmd/builder@v0.156.0 --config builder-config.yaml
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

step "compose configurations"
export E2E_RUN_ID=ci-validation OPENAI_API_KEY=validation-only OPENCODE_API_KEY=validation-only COPILOT_PROVIDER_API_KEY=validation-only ANTHROPIC_AUTH_TOKEN=validation-only
docker compose -f compose.e2e-codex.yaml config --quiet
# The real key reaches responses-proxy only; the Codex agent gets a placeholder,
# because config.toml's env_key just needs a value the proxy will ignore.
docker compose -f compose.e2e-codex.yaml config --format json \
  | jq -e '.services["responses-proxy"].environment.OPENAI_API_KEY == "validation-only"
           and .services.agent.environment.OPENAI_API_KEY != "validation-only"'
# ANTHROPIC_API_KEY must stay absent so it cannot shadow ANTHROPIC_AUTH_TOKEN.
docker compose -f compose.e2e-claude.yaml config --format json \
  | jq -e '.services.agent.environment.ANTHROPIC_BASE_URL == "https://api.z.ai/api/anthropic" and (.services.agent.environment | has("ANTHROPIC_API_KEY") | not)'
# Each of the remaining stacks receives exactly one credential: the z.ai key as
# OPENAI_API_KEY. No Anthropic credential may leak in.
for stack in openai strands; do
  docker compose -f "compose.e2e-$stack.yaml" config --quiet
  docker compose -f "compose.e2e-$stack.yaml" config --format json \
    | jq -e '.services.agent.environment.OPENAI_API_KEY == "validation-only"
             and (.services.agent.environment | has("ANTHROPIC_AUTH_TOKEN") | not)'
done
# The OpenCode e2e stack receives exactly one credential: an OpenCode Go key
# as OPENCODE_API_KEY. No Anthropic or z.ai credential may leak in.
docker compose -f compose.e2e-opencode.yaml config --quiet
docker compose -f compose.e2e-opencode.yaml config --format json \
  | jq -e '.services.agent.environment.OPENCODE_API_KEY == "validation-only"
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("ANTHROPIC_AUTH_TOKEN") | not)'
# The Copilot e2e stack receives exactly one credential: the BYOK provider key
# as COPILOT_PROVIDER_API_KEY, alongside the provider/model/OTEL configuration.
# No OpenAI or Anthropic credential may leak in.
docker compose -f compose.e2e-copilot.yaml config --quiet
docker compose -f compose.e2e-copilot.yaml config --format json \
  | jq -e '.services.agent.environment.COPILOT_PROVIDER_API_KEY == "validation-only"
           and (.services.agent.environment
                | [has("COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_BASE_URL",
                       "COPILOT_MODEL", "COPILOT_OTEL_ENABLED",
                       "OTEL_EXPORTER_OTLP_ENDPOINT",
                       "OTEL_EXPORTER_OTLP_PROTOCOL")]
                | all)
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("ANTHROPIC_AUTH_TOKEN") | not)
           and (.services.agent.environment | has("ANTHROPIC_API_KEY") | not)'
# The Pi e2e stack receives exactly one credential: the z.ai key as ANTHROPIC_AUTH_TOKEN; no other stack's credential may leak in.
docker compose -f compose.e2e-pi.yaml config --quiet
docker compose -f compose.e2e-pi.yaml config --format json \
  | jq -e '.services.agent.environment.ANTHROPIC_AUTH_TOKEN == "validation-only"
           and (.services.agent.environment | has("OPENAI_API_KEY") | not)
           and (.services.agent.environment | has("ANTHROPIC_API_KEY") | not)
           and (.services.agent.environment | has("OPENCODE_API_KEY") | not)
           and (.services.agent.environment | has("COPILOT_PROVIDER_API_KEY") | not)'

step "container images"
docker build --tag otelcol-coding-agents:check .
docker build --tag codex-e2e:check e2e/codex
docker build --tag claude-e2e:check e2e/claude
# The Codex stack cannot run without this one, and it is the only image built
# from a git ref rather than an immutable registry artifact, so a vanished fork
# branch or a broken dependency must fail here rather than during someone's
# paid e2e run.
docker build --tag responses-proxy-e2e:check e2e/responses-proxy
docker build --tag openai-adhoc-e2e:check e2e/openai-adhoc
docker build --tag strands-e2e:check e2e/strands
docker build --tag opencode-e2e:check e2e/opencode
docker build --tag copilot-e2e:check e2e/copilot
docker build --tag pi-e2e:check e2e/pi

step "goreleaser"
goreleaser check

printf '\nALL CHECKS PASSED\n'
