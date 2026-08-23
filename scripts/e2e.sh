#!/usr/bin/env bash
set -euo pipefail

# OPENAI_API_KEY holds the z.ai API key. It is passed only to the responses-proxy
# container, which makes the downstream z.ai call; the Codex container gets a
# placeholder (see compose.e2e-codex.yaml).
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid Codex session." >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-codex.yaml)
# Long-running services to bring up before the agent: the collector plus the
# responses-proxy that lets Codex reach z.ai's chat-only endpoint.
support_services=(collector responses-proxy)
e2e_run codex
