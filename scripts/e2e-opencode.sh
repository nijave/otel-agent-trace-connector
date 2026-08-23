#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the OpenCode Go key as OPENCODE_API_KEY.
if [[ -z "${OPENCODE_API_KEY:-}" ]]; then
  echo "OPENCODE_API_KEY (your OpenCode Go API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the OpenCode validation path in the shared validator.
export E2E_AGENT=opencode

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

# The OpenCode stack only needs the shared collector; it talks to OpenCode Go directly.
# shellcheck disable=SC2034
compose_files=(-f compose.e2e-opencode.yaml)
# shellcheck disable=SC2034
support_services=(collector)
e2e_run opencode
