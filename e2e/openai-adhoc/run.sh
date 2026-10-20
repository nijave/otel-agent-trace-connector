#!/bin/sh
set -eu

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "OPENAI_API_KEY (your OpenRouter API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# One run. opentelemetry-util-genai >= 1.0b0 ignores
# OTEL_SEMCONV_STABILITY_OPT_IN (is_experimental_mode() always returns true), so
# the pinned util-genai 1.1b0 always emits on the opentelemetry.util.genai.handler
# scope with gen_ai.provider.name; a second opt-in run produced identical spans.
OTEL_SERVICE_NAME=openai-adhoc \
  timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py
