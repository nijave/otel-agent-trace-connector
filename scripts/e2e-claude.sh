#!/usr/bin/env bash
set -euo pipefail

# The container receives exactly one credential: an ephemeral Bedrock API key.
# Generate a short-lived one from your host AWS credentials with
# scripts/e2e-claude-bedrock.sh, or supply your own AWS_BEARER_TOKEN_BEDROCK.
if [[ -z "${AWS_REGION:-}" ]]; then
  echo "AWS_REGION is required; this test runs a real paid Claude model on Bedrock." >&2
  exit 2
fi
if [[ -z "${AWS_BEARER_TOKEN_BEDROCK:-}" ]]; then
  echo "AWS_BEARER_TOKEN_BEDROCK is required; run scripts/e2e-claude-bedrock.sh to mint one." >&2
  exit 2
fi

export E2E_CLAUDE_MODEL="${E2E_CLAUDE_MODEL:-us.anthropic.claude-haiku-4-5-20251001-v1:0}"
# Selects the Claude validation path in the shared validator (see compose.e2e-base.yaml).
export E2E_AGENT=claude_code

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-claude.yaml)
e2e_run claude
