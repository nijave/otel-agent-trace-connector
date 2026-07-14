#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the z.ai API key, passed to Claude Code
# as ANTHROPIC_AUTH_TOKEN against z.ai's Anthropic-compatible endpoint.
if [[ -z "${ANTHROPIC_AUTH_TOKEN:-}" ]]; then
  echo "ANTHROPIC_AUTH_TOKEN (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

export E2E_CLAUDE_MODEL="${E2E_CLAUDE_MODEL:-glm-4.7}"
# Selects the Claude validation path in the shared validator (see compose.e2e-base.yaml).
export E2E_AGENT=claude_code

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-claude.yaml)
# The Claude stack only needs the shared collector; Claude Code talks to z.ai's
# Anthropic-compatible endpoint directly, so no proxy is required.
support_services=(collector)
e2e_run claude
