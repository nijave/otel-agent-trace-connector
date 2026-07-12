#!/bin/sh
set -eu

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "ANTHROPIC_API_KEY is required" >&2
  exit 2
fi

if [ -n "${E2E_EXTRA_CA_BUNDLE:-}" ]; then
  if [ ! -f "${E2E_EXTRA_CA_BUNDLE}" ] || [ ! -r "${E2E_EXTRA_CA_BUNDLE}" ]; then
    echo "E2E_EXTRA_CA_BUNDLE is not a readable file: ${E2E_EXTRA_CA_BUNDLE}" >&2
    exit 2
  fi
  cat /etc/ssl/certs/ca-certificates.crt "${E2E_EXTRA_CA_BUNDLE}" \
    > /tmp/e2e-ca-certificates.crt
  export SSL_CERT_FILE=/tmp/e2e-ca-certificates.crt
  export NODE_EXTRA_CA_CERTS=/tmp/e2e-ca-certificates.crt
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  claude --bare -p \
    --model "${E2E_CLAUDE_MODEL:-haiku}" \
    --tools Bash \
    --allowedTools "Bash(printf claude-otel-e2e)" \
    --strict-mcp-config \
    --permission-mode dontAsk \
    --max-turns "${E2E_CLAUDE_MAX_TURNS:-3}" \
    --max-budget-usd "${E2E_CLAUDE_MAX_BUDGET_USD:-0.25}" \
    --no-session-persistence \
    --output-format json \
    "Use the Bash tool exactly once to run 'printf claude-otel-e2e'. Then reply with only: done."
