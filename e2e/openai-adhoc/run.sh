#!/bin/sh
set -eu

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Default mode: semconv v1.30.0 (gen_ai.system on spans).
OTEL_SERVICE_NAME=openai-adhoc-legacy \
  timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py

# Experimental mode: semconv v1.37 via opentelemetry-util-genai
# (gen_ai.provider.name on spans).
OTEL_SERVICE_NAME=openai-adhoc-latest \
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental \
  timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py
