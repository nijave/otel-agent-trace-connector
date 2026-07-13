#!/bin/sh
set -eu

if [ "${CLAUDE_CODE_USE_BEDROCK:-}" != "1" ]; then
  echo "CLAUDE_CODE_USE_BEDROCK=1 is required" >&2
  exit 2
fi
if [ -z "${AWS_REGION:-}" ]; then
  echo "AWS_REGION is required" >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  claude --bare -p \
    --model "${E2E_CLAUDE_MODEL:-us.anthropic.claude-haiku-4-5-20251001-v1:0}" \
    --tools Bash \
    --allowedTools "Bash(printf claude-otel-e2e)" \
    --strict-mcp-config \
    --permission-mode dontAsk \
    --max-turns "${E2E_CLAUDE_MAX_TURNS:-3}" \
    --max-budget-usd "${E2E_CLAUDE_MAX_BUDGET_USD:-0.25}" \
    --no-session-persistence \
    --output-format json \
    "Use the Bash tool exactly once to run 'printf claude-otel-e2e'. Then reply with only: done."
