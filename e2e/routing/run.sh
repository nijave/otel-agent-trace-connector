#!/bin/sh
set -eu

: "${GATEWAY_ENDPOINT:?GATEWAY_ENDPOINT must be set}"
: "${E2E_RUN_ID:?E2E_RUN_ID must be set}"
count="${CONVERSATIONS:-8}"

# The contrib gateway image is distroless (no shell, no curl), so compose
# cannot healthcheck it; probe readiness with an empty valid OTLP export.
attempt=0
until curl --silent --output /dev/null --fail --max-time 2 \
    --header 'Content-Type: application/json' --data '{"resourceLogs":[]}' \
    "${GATEWAY_ENDPOINT}/v1/logs"; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    echo "gateway never became ready" >&2
    exit 1
  fi
  sleep 1
done

# Replay the pinned codex capture once per synthetic conversation, rewriting
# every conversation.id attribute. One fixture record carries no attributes at
# all; it hashes to the missing-attribute bucket on one backend and the
# connector ignores it, so it cannot split a conversation.
n=1
while [ "$n" -le "$count" ]; do
  conv="routing-${E2E_RUN_ID}-${n}"
  while IFS= read -r line; do
    printf '%s' "$line" \
      | jq -c --arg id "$conv" \
          '(.resourceLogs[]?.scopeLogs[]?.logRecords[]?.attributes[]?
            | select(.key == "conversation.id") | .value.stringValue) |= $id' \
      | curl --silent --output /dev/null --fail \
          --header 'Content-Type: application/json' --data-binary @- \
          "${GATEWAY_ENDPOINT}/v1/logs"
  done < /fixture/codex-native-logs.json
  n=$((n + 1))
done
echo "replayed ${count} conversations"
