#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the provider key as COPILOT_PROVIDER_API_KEY.
if [[ -z "${COPILOT_PROVIDER_API_KEY:-}" ]]; then
  echo "COPILOT_PROVIDER_API_KEY is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the Copilot validation path in the shared validator.
export E2E_AGENT=copilot

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

# The Copilot stack only needs the shared collector; it talks to the configured
# provider directly.
# shellcheck disable=SC2034
compose_files=(-f compose.e2e-copilot.yaml)
# shellcheck disable=SC2034
support_services=(collector)
e2e_run copilot
