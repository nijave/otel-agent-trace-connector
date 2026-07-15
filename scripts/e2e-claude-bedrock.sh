#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${AWS_REGION:-}" ]]; then
  echo "AWS_REGION is required" >&2
  exit 2
fi
if ! command -v uv >/dev/null 2>&1; then
  echo "uv is required to generate a short-lived Bedrock bearer token" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export AWS_BEARER_TOKEN_BEDROCK
AWS_BEARER_TOKEN_BEDROCK="$(
  uv run --quiet --no-project \
    --with 'aws-bedrock-token-generator==1.1.0' \
    python "${script_dir}/bedrock-token.py"
)"
if [[ "${AWS_BEARER_TOKEN_BEDROCK}" != bedrock-api-key-* ]]; then
  echo "Bedrock token generator returned an unexpected value" >&2
  exit 1
fi

# The container only ever receives the variables allowlisted in
# compose.e2e-claude.yaml's `environment:` block (this token plus AWS_REGION). Docker
# Compose does not forward other host variables, so host AWS credentials never
# reach the container -- there is no denylist to maintain here.
exec "${script_dir}/e2e-claude.sh"
