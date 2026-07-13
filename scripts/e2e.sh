#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is required; this test runs a real paid Codex session." >&2
  exit 2
fi

export E2E_CODEX_MODEL="${E2E_CODEX_MODEL:-gpt-5.1-codex-mini}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.yaml)
e2e_run codex
