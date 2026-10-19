#!/usr/bin/env bash
# shellcheck disable=SC2034
set -euo pipefail

# The container receives one credential: the OpenCode Go key as OPENCODE_API_KEY,
# read by pi's built-in opencode-go provider (which also sends Go's session header).
if [[ -z "${OPENCODE_API_KEY:-}" ]]; then
  echo "OPENCODE_API_KEY (your OpenCode Go API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the Pi validation path in the shared validator (see compose.e2e-base.yaml).
export E2E_AGENT=pi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

# shellcheck disable=SC2034
compose_files=(-f compose.e2e-pi.yaml)
# The Pi stack only needs the shared collector; pi talks to OpenCode Go directly,
# so no proxy is required.
support_services=(collector)
e2e_run pi
