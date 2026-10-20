#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the OpenCode Go key, passed to Claude
# Code as ANTHROPIC_API_KEY against OpenCode Go's Anthropic-compatible endpoint
# (sent as x-api-key; a bearer token is rejected).
if [[ -z "${OPENCODE_API_KEY:-}" ]]; then
  echo "OPENCODE_API_KEY (your OpenCode Go API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the Claude validation path in the shared validator (see compose.e2e-base.yaml).
export E2E_AGENT=claude_code

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-claude.yaml)
# The Claude stack only needs the shared collector; Claude Code talks to OpenCode
# Go's Anthropic-compatible endpoint directly, so no proxy is required.
support_services=(collector)
e2e_run claude
