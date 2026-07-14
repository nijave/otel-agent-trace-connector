#!/usr/bin/env bash
set -euo pipefail

# OPENAI_API_KEY holds the z.ai API key; Codex reads it via the z.ai model
# provider in e2e/codex/config.toml (env_key).
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid Codex session." >&2
  exit 2
fi

export E2E_CODEX_MODEL="${E2E_CODEX_MODEL:-glm-4.7}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-codex.yaml)
e2e_run codex
