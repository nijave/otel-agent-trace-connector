#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the OpenCode Go key as OPENCODE_API_KEY,
# sent by Codex as the bearer token to OpenCode Go's Responses API endpoint.
if [[ -z "${OPENCODE_API_KEY:-}" ]]; then
  echo "OPENCODE_API_KEY (your OpenCode Go API key) is required; this test runs a real paid model." >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-codex.yaml)
# Long-running services to bring up before the agent: the shared collector.
support_services=(collector)
e2e_run codex
