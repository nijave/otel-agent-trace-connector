#!/usr/bin/env bash
# shellcheck disable=SC2034
set -euo pipefail

# The container receives one credential: the z.ai API key, used directly by
# the openai SDK against z.ai's OpenAI-compatible endpoint.
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the openai-adhoc validation path in the shared validator.
export E2E_AGENT=openai_adhoc

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

# shellcheck disable=SC2034
compose_files=(-f compose.e2e-openai.yaml); support_services=(collector)
e2e_run openai
