#!/bin/sh
set -eu

if [ -z "${ANTHROPIC_BASE_URL:-}" ] || [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY are required (OpenCode Go endpoint + key)" >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  claude --bare -p \
    --model "${E2E_CLAUDE_MODEL:-claude-haiku-5-5}" \
    --tools Bash \
    --allowedTools "Bash(printf claude-otel-e2e)" \
    --strict-mcp-config \
    --permission-mode dontAsk \
    --max-turns "${E2E_CLAUDE_MAX_TURNS:-3}" \
    --max-budget-usd "${E2E_CLAUDE_MAX_BUDGET_USD:-0.25}" \
    --no-session-persistence \
    --output-format json \
    "Use the Bash tool exactly once to run 'printf claude-otel-e2e'. Then reply with only: done."
