#!/usr/bin/env bash
# shellcheck disable=SC2034
set -euo pipefail

if [[ -z "${LLM_API_KEY:-}" ]]; then
  echo "LLM_API_KEY is required; this test runs a real paid model." >&2
  exit 2
fi

export LLM_MODEL="${LLM_MODEL:-anthropic/claude-sonnet-4-5}"
# Selects the OpenHands validation path in the shared validator (see
# compose.e2e-base.yaml).
export E2E_AGENT=openhands

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

# shellcheck disable=SC2034
compose_files=(-f compose.e2e-openhands.yaml)
# The OpenHands stack only needs the shared collector; the SDK talks to the
# LLM provider directly through litellm, so no proxy is required.
support_services=(collector)
e2e_run openhands
