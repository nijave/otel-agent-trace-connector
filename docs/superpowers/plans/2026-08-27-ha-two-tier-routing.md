# Two-Tier HA Routing (Option B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship deployable, proven artifacts for Option B of the HA analysis: a stateless contrib gateway tier that consistent-hash-routes on conversation attributes in front of N replicas of this distribution.

**Architecture:** Tier 1 is stock `otel/opentelemetry-collector-contrib:0.159.0` running an OTLP receiver and the `load_balancing` exporter with `routing_key: attributes` on `conversation.id` and `cursor.conversation.id`. Tier 2 is this repository's distribution, unchanged, behind a headless Service. A zero-API-cost compose e2e proves the routing semantics: it replays the pinned codex fixture as N synthetic conversations through a real gateway into two tier-2 replicas and asserts each conversation lands whole on exactly one backend. Kubernetes manifests under `examples/k8s-ha/` carry the same two configs with the k8s resolver and its RBAC.

**Tech Stack:** OpenTelemetry Collector v0.159.0 (contrib image + this distribution), Docker Compose, Go (e2e validator), plain Kubernetes manifests.

**Spec:** `docs/multi-instance-ha.md` (Option B). Read it first; this plan builds its recommendation and inherits its caveats.

## Global Constraints

- Prefix EVERY go command with `GOTOOLCHAIN=auto` (system go is older than the module floor; unprefixed go commands fail).
- `GOTOOLCHAIN=auto ./scripts/check.sh` must end `ALL CHECKS PASSED` before any push.
- Stage explicit paths only; never blanket `git add`.
- Collector version everywhere is `0.159.0` — the pin in `builder-config.yaml`, `compose.e2e-base.yaml`, and the root `Dockerfile` (`OCB_VERSION`).
- The exporter component id at v0.159.0 is `load_balancing` (checked against the pinned contrib binary with `docker run --rm otel/opentelemetry-collector-contrib:0.159.0 components`; older releases spelled it `loadbalancing`).
- No connector code changes. The spec states Option B needs none; if a task appears to need one, stop and re-read the spec.
- The contrib image is distroless: no shell, no curl, so no compose exec healthcheck for the gateway. The replay agent probes readiness instead (Task 1).
- The routing e2e costs nothing (no model calls) but stays opt-in like every other stack; CI and `check.sh` only build and config-check it.
- The repository publishes no container image. Kubernetes manifests reference a placeholder image with a build-and-push note (Task 3); do not invent a registry path.

## File map

| File | Responsibility |
| --- | --- |
| `e2e/routing/tier2.yaml` | Tier-2 collector config for the e2e: this distribution with env-parameterized canonical output path |
| `e2e/routing/gateway.yaml` | Tier-1 gateway config for the e2e: `load_balancing` over a static resolver |
| `e2e/routing/Dockerfile`, `e2e/routing/run.sh` | Replay agent: posts the pinned codex fixture as N synthetic conversations |
| `compose.e2e-routing.yaml` | gateway + collector-a + collector-b + agent |
| `e2e/validator/validator.go` (append) | Pure routing-home extraction, unit-testable |
| `e2e/validator/validator_test.go` (append) | Unit test for the pure function |
| `e2e/validator/routing_live_test.go` | e2e-tagged affinity test over both output files |
| `scripts/e2e-routing.sh` | Orchestration (variant of `scripts/lib-e2e.sh` with two output files) |
| `scripts/check.sh` (edit) | Config checks for both new collector configs + no-credential assertion for the routing stack |
| `examples/k8s-ha/*.yaml` + `README.md` | Kubernetes shape: tier-1 (deployment, service, RBAC, config), tier-2 (deployment, headless service, config) |
| `docs/multi-instance-ha.md`, `e2e/README.md`, `README.md` (edits) | Status + operator docs |

---

### Task 1: Routing e2e stack (configs, replay agent, compose file)

**Files:**
- Create: `e2e/routing/tier2.yaml`
- Create: `e2e/routing/gateway.yaml`
- Create: `e2e/routing/run.sh`
- Create: `e2e/routing/Dockerfile`
- Create: `compose.e2e-routing.yaml`

**Interfaces:**
- Consumes: `collector-config.yaml` (tier-2 config mirrors it), `connector/codingagentconnector/internal/codex/testdata/codex-native-logs.json` (replay source; every record except one attribute-less degenerate carries the `conversation.id` log attribute).
- Produces (Task 2 relies on these exactly): compose service names `gateway`, `collector-a`, `collector-b`, `agent`; output files `.e2e-output/canonical-a.json` and `.e2e-output/canonical-b.json`; synthetic conversation ids `routing-${E2E_RUN_ID}-<n>` for n = 1..`${CONVERSATIONS:-8}`; resource attribute `e2e.run.id` = `${E2E_RUN_ID}` on all canonical output.

- [ ] **Step 1: Write the tier-2 config**

`e2e/routing/tier2.yaml` — the bundled `collector-config.yaml` minus raw-file exporters, with the canonical output path parameterized so two replicas can share one volume:

```yaml
# Tier-2 config for the routing e2e: this distribution, unchanged in behavior
# from collector-config.yaml, with the canonical file path env-parameterized so
# collector-a and collector-b write distinct files into the shared volume.
extensions:
  health_check:
    endpoint: 0.0.0.0:13133

receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

connectors:
  coding_agent:
    turn_timeout: 30s
    reorder_window: 2s
    max_active_turns: 100
    max_events_per_turn: 1000
  coding_agent/claude:

processors:
  resource/e2e:
    attributes:
      - key: e2e.run.id
        value: ${env:E2E_RUN_ID:-unscoped}
        action: upsert
  batch:
    timeout: 500ms

exporters:
  file/canonical:
    path: ${env:CANONICAL_OUT:-/output/canonical-traces.json}
    format: json
    flush_interval: 100ms

service:
  extensions: [health_check]
  pipelines:
    logs/vendor:
      receivers: [otlp]
      processors: [resource/e2e, batch]
      exporters: [coding_agent]
    traces/native:
      receivers: [otlp]
      processors: [resource/e2e, batch]
      exporters: [coding_agent/claude]
    traces/canonical:
      receivers: [coding_agent, coding_agent/claude]
      # resource/e2e must re-stamp here: the connector's canonical resource
      # contract strips non-contract attributes, including the pre-connector
      # e2e.run.id stamp the validator correlates on.
      processors: [resource/e2e, batch]
      exporters: [file/canonical]
```

- [ ] **Step 2: Write the gateway config**

`e2e/routing/gateway.yaml` — stock contrib, consistent-hash routing on the conversation attributes, resilience enabled deliberately (the spec: sub-exporter resilience defaults to off in this exporter):

```yaml
# Tier-1 gateway for the routing e2e: stock otelcol-contrib. Routes each
# record by conversation attribute so every event of one conversation reaches
# the same tier-2 replica. The static resolver stands in for the k8s resolver
# the examples/k8s-ha manifests use; the routing semantics are identical.
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

exporters:
  load_balancing:
    routing_key: attributes
    routing_attributes:
      - conversation.id
      - cursor.conversation.id
    protocol:
      otlp:
        tls:
          insecure: true
        retry_on_failure:
          enabled: true
        sending_queue:
          enabled: true
    resolver:
      static:
        hostnames:
          - collector-a:4317
          - collector-b:4317

service:
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [load_balancing]
    traces:
      receivers: [otlp]
      exporters: [load_balancing]
```

- [ ] **Step 3: Write the replay agent**

`e2e/routing/run.sh`:

```sh
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
```

`e2e/routing/Dockerfile`:

```dockerfile
FROM alpine:3.22
RUN apk add --no-cache curl jq
COPY run.sh /run.sh
RUN chmod +x /run.sh
ENTRYPOINT ["/run.sh"]
```

- [ ] **Step 4: Write the compose stack**

`compose.e2e-routing.yaml` — deliberately not including `compose.e2e-base.yaml`: this stack needs two tier-2 replicas with per-replica env, not the shared single collector.

```yaml
# Two-tier consistent-hash routing e2e (spec: docs/multi-instance-ha.md,
# Option B). Costs nothing: the "agent" replays the pinned codex fixture as
# synthetic conversations through a stock contrib gateway running the
# load_balancing exporter; the validator asserts each conversation lands whole
# on exactly one tier-2 replica. Does not include compose.e2e-base.yaml
# because the topology needs two collectors with distinct output paths.
services:
  gateway:
    image: otel/opentelemetry-collector-contrib:0.159.0
    command: ["--config=/etc/otelcol/gateway.yaml"]
    volumes:
      - ./e2e/routing/gateway.yaml:/etc/otelcol/gateway.yaml:ro
    # Distroless image: no exec healthcheck possible; the agent probes it.
    depends_on:
      collector-a:
        condition: service_healthy
      collector-b:
        condition: service_healthy

  collector-a:
    build:
      context: .
      args:
        OCB_VERSION: "0.159.0"
    command: ["--config=/etc/otelcol/tier2.yaml"]
    volumes:
      - ./e2e/routing/tier2.yaml:/etc/otelcol/tier2.yaml:ro
      - ./.e2e-output:/output
    environment:
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-routing.sh}"
      CANONICAL_OUT: /output/canonical-a.json
    healthcheck:
      test: ["CMD", "curl", "--fail", "--silent", "http://localhost:13133/"]
      interval: 1s
      timeout: 2s
      retries: 30

  collector-b:
    build:
      context: .
      args:
        OCB_VERSION: "0.159.0"
    command: ["--config=/etc/otelcol/tier2.yaml"]
    volumes:
      - ./e2e/routing/tier2.yaml:/etc/otelcol/tier2.yaml:ro
      - ./.e2e-output:/output
    environment:
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-routing.sh}"
      CANONICAL_OUT: /output/canonical-b.json
    healthcheck:
      test: ["CMD", "curl", "--fail", "--silent", "http://localhost:13133/"]
      interval: 1s
      timeout: 2s
      retries: 30

  agent:
    build:
      context: e2e/routing
    volumes:
      - ./connector/codingagentconnector/internal/codex/testdata/codex-native-logs.json:/fixture/codex-native-logs.json:ro
    environment:
      GATEWAY_ENDPOINT: "http://gateway:4318"
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-routing.sh}"
      CONVERSATIONS: "${CONVERSATIONS:-8}"
    depends_on:
      gateway:
        condition: service_started
```

- [ ] **Step 5: Check both configs against their real binaries**

```bash
GOTOOLCHAIN=auto go run go.opentelemetry.io/collector/cmd/builder@v0.159.0 --config builder-config.yaml
CANONICAL_OUT=/tmp/routing-validate.json ./dist/otelcol-coding-agents validate --config e2e/routing/tier2.yaml
docker run --rm -v "$PWD/e2e/routing/gateway.yaml:/gateway.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.159.0 validate --config /gateway.yaml
docker compose -f compose.e2e-routing.yaml config --quiet
```

Expected: all four exit 0. If the contrib binary rejects a key under `load_balancing`, the binary wins over this plan and over the spec — adjust the config to what v0.159.0 accepts and record the difference in the commit body.

- [ ] **Step 6: Commit**

```bash
git add e2e/routing/tier2.yaml e2e/routing/gateway.yaml e2e/routing/run.sh e2e/routing/Dockerfile compose.e2e-routing.yaml
git commit -m "feat(e2e): add two-tier routing stack (configs, replay agent, compose)"
```

---

### Task 2: Affinity validator, orchestration script, check.sh integration

**Files:**
- Edit: `e2e/validator/validator.go` (append the pure function)
- Edit: `e2e/validator/validator_test.go` (append the unit test)
- Create: `e2e/validator/routing_live_test.go`
- Create: `scripts/e2e-routing.sh`
- Edit: `scripts/check.sh` (config checks + credential assertion)

**Interfaces:**
- Consumes from Task 1: output files `.e2e-output/canonical-a.json` / `canonical-b.json`, conversation id format `routing-<runID>-<n>`, service names for compose.
- Consumes from the existing validator: `validateTraceFile(path, runID string, validate func(ptrace.Traces, string) error) error` (merges JSONL batches) and `collectRunSpans(traces ptrace.Traces, runID string) []ptrace.Span` (filters by `e2e.run.id`), both in `e2e/validator/validator.go`.
- Produces: `routingHomes(traces ptrace.Traces, runID string) map[string]routingHome` with `type routingHome struct { HasRoot, HasChat bool }`; env contract for the live test: `CANONICAL_FILE_A`, `CANONICAL_FILE_B`, `E2E_RUN_ID`, `CONVERSATIONS`.

- [ ] **Step 1: Write the failing unit test**

Append to `e2e/validator/validator_test.go` (untagged file — runs in normal `go test`):

```go
func TestRoutingHomes(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run1")
	ss := rs.ScopeSpans().AppendEmpty()

	traceID := pcommon.TraceID([16]byte{1})
	root := ss.Spans().AppendEmpty()
	root.SetName("invoke_agent codex")
	root.SetTraceID(traceID)
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.conversation.id", "routing-run1-3")

	chat := ss.Spans().AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.SetTraceID(traceID)
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")

	// A root from another run must not appear.
	other := traces.ResourceSpans().AppendEmpty()
	other.Resource().Attributes().PutStr("e2e.run.id", "other")
	otherSpan := other.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	otherSpan.SetName("invoke_agent codex")
	otherSpan.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	otherSpan.Attributes().PutStr("gen_ai.conversation.id", "routing-other-1")

	homes := routingHomes(traces, "run1")
	if len(homes) != 1 {
		t.Fatalf("expected exactly one conversation, got %d", len(homes))
	}
	home, ok := homes["routing-run1-3"]
	if !ok {
		t.Fatal("conversation routing-run1-3 missing")
	}
	if !home.HasRoot || !home.HasChat {
		t.Fatalf("expected root and chat, got %+v", home)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd e2e/validator && GOTOOLCHAIN=auto go test -run '^TestRoutingHomes$' ./... ; cd ../..`
Expected: FAIL to compile — `routingHomes` and `routingHome` undefined.

- [ ] **Step 3: Write the pure function**

Append to `e2e/validator/validator.go`:

```go
// routingHome records what one tier-2 replica's canonical output holds for a
// synthetic routing conversation: the invoke_agent root and, joined by trace
// ID (conversation.id lives only on the root), at least one chat child.
type routingHome struct {
	HasRoot bool
	HasChat bool
}

// routingHomes maps each routing conversation id found in the run's spans to
// what this file holds for it. The affinity test computes one map per tier-2
// output file: a conversation split across replicas shows up in both maps.
func routingHomes(traces ptrace.Traces, runID string) map[string]routingHome {
	spans := collectRunSpans(traces, runID)
	homes := map[string]routingHome{}
	traceConv := map[pcommon.TraceID]string{}
	for _, span := range spans {
		if op, ok := span.Attributes().Get("gen_ai.operation.name"); !ok || op.Str() != "invoke_agent" {
			continue
		}
		conv, ok := span.Attributes().Get("gen_ai.conversation.id")
		if !ok || !strings.HasPrefix(conv.Str(), "routing-"+runID+"-") {
			continue
		}
		home := homes[conv.Str()]
		home.HasRoot = true
		homes[conv.Str()] = home
		traceConv[span.TraceID()] = conv.Str()
	}
	for _, span := range spans {
		op, ok := span.Attributes().Get("gen_ai.operation.name")
		if !ok || op.Str() != "chat" {
			continue
		}
		conv, ok := traceConv[span.TraceID()]
		if !ok {
			continue
		}
		home := homes[conv]
		home.HasChat = true
		homes[conv] = home
	}
	return homes
}
```

- [ ] **Step 4: Run the unit test to verify it passes**

Run: `cd e2e/validator && GOTOOLCHAIN=auto go test -run '^TestRoutingHomes$' ./... ; cd ../..`
Expected: PASS

- [ ] **Step 5: Write the live affinity test**

`e2e/validator/routing_live_test.go`:

```go
//go:build e2e

package validator

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TestRoutingAffinity checks the two-tier routing e2e: every synthetic
// conversation replayed through the gateway must land whole (root + chat) on
// exactly one tier-2 replica. Spread across replicas is logged, not asserted:
// with a consistent hash, all conversations legally landing on one backend is
// improbable but valid.
func TestRoutingAffinity(t *testing.T) {
	pathA := os.Getenv("CANONICAL_FILE_A")
	pathB := os.Getenv("CANONICAL_FILE_B")
	runID := os.Getenv("E2E_RUN_ID")
	if pathA == "" || pathB == "" || runID == "" {
		t.Fatal("CANONICAL_FILE_A, CANONICAL_FILE_B, and E2E_RUN_ID must be set")
	}
	count, err := strconv.Atoi(os.Getenv("CONVERSATIONS"))
	if err != nil || count < 1 {
		t.Fatal("CONVERSATIONS must be a positive integer")
	}

	// Turns finalize via the 30s inactivity timeout after the replay stops,
	// then the file exporter flushes; poll well past both.
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = checkAffinity(pathA, pathB, runID, count)
		if lastErr == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("routing affinity not satisfied: %v", lastErr)
}

func checkAffinity(pathA, pathB, runID string, count int) error {
	homesA, err := fileRoutingHomes(pathA, runID)
	if err != nil {
		return err
	}
	homesB, err := fileRoutingHomes(pathB, runID)
	if err != nil {
		return err
	}
	spreadA, spreadB := 0, 0
	for n := 1; n <= count; n++ {
		conv := fmt.Sprintf("routing-%s-%d", runID, n)
		a, inA := homesA[conv]
		b, inB := homesB[conv]
		switch {
		case inA && inB:
			return fmt.Errorf("conversation %s split across both replicas", conv)
		case !inA && !inB:
			return fmt.Errorf("conversation %s missing from both replicas", conv)
		case inA:
			if !a.HasRoot || !a.HasChat {
				return fmt.Errorf("conversation %s incomplete on replica a: %+v", conv, a)
			}
			spreadA++
		default:
			if !b.HasRoot || !b.HasChat {
				return fmt.Errorf("conversation %s incomplete on replica b: %+v", conv, b)
			}
			spreadB++
		}
	}
	fmt.Printf("routing spread: replica-a=%d replica-b=%d\n", spreadA, spreadB)
	return nil
}

func fileRoutingHomes(path, runID string) (map[string]routingHome, error) {
	var homes map[string]routingHome
	err := validateTraceFile(path, runID, func(traces ptrace.Traces, runID string) error {
		homes = routingHomes(traces, runID)
		return nil
	})
	return homes, err
}
```

- [ ] **Step 6: Compile-check the e2e-tagged test**

Run: `cd e2e/validator && GOTOOLCHAIN=auto go vet -tags=e2e ./... ; cd ../..`
Expected: exit 0.

- [ ] **Step 7: Write the orchestration script**

`scripts/e2e-routing.sh` (mirror the header style of `scripts/e2e-openhands.sh`; mark executable):

```bash
#!/usr/bin/env bash
set -euo pipefail

# Zero-API-cost e2e: replays the pinned codex fixture through a contrib
# gateway into two tier-2 replicas and asserts per-conversation affinity.
# No model credentials needed.

compose_files=(-f compose.e2e-routing.yaml)
run_compose() { docker compose "${compose_files[@]}" "$@"; }

export E2E_RUN_ID="${E2E_RUN_ID:-routing-otel-$(date +%s)-$$}"
export CONVERSATIONS="${CONVERSATIONS:-8}"
mkdir -p .e2e-output
rm -f .e2e-output/canonical-a.json .e2e-output/canonical-b.json
trap 'run_compose down --remove-orphans' EXIT
run_compose build
run_compose up --detach --wait collector-a collector-b gateway
run_compose run --rm --no-deps agent
CANONICAL_FILE_A="${PWD}/.e2e-output/canonical-a.json" \
CANONICAL_FILE_B="${PWD}/.e2e-output/canonical-b.json" \
  go test -tags=e2e -count=1 -run '^TestRoutingAffinity$' ./e2e/validator/
```

Then: `chmod +x scripts/e2e-routing.sh` (every sibling e2e entrypoint is 100755; a missing exec bit here already bit the openhands stack once).

- [ ] **Step 8: Wire check.sh**

In `scripts/check.sh`, two edits:

1. In the "collector build and config validation" step, after the `examples/otelcol-s3.yaml` line, add:

```bash
CANONICAL_OUT=/tmp/otelcol-routing-validate.json \
  ./dist/otelcol-coding-agents validate --config e2e/routing/tier2.yaml
docker run --rm -v "$PWD/e2e/routing/gateway.yaml:/gateway.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.159.0 validate --config /gateway.yaml
```

2. In the "compose configurations" step, after the openhands assertion, add the routing stack's credential contract (it must receive none):

```bash
# The routing e2e stack replays a committed fixture; it receives no
# credential at all. Every stack key must stay absent.
docker compose -f compose.e2e-routing.yaml config --format json \
  | jq -e '.services.agent.environment
           | (has("OPENAI_API_KEY") or has("ANTHROPIC_AUTH_TOKEN")
              or has("ANTHROPIC_API_KEY") or has("OPENCODE_API_KEY")
              or has("COPILOT_PROVIDER_API_KEY") or has("LLM_API_KEY"))
           | not'
```

- [ ] **Step 9: Run the live routing e2e**

Run: `GOTOOLCHAIN=auto ./scripts/e2e-routing.sh`
Expected: agent prints `replayed 8 conversations`; the validator prints a `routing spread:` line and passes. Run this before the PR (per-harness e2e rule); it costs nothing.

- [ ] **Step 10: Run the full gate**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: ends `ALL CHECKS PASSED`.

- [ ] **Step 11: Commit**

```bash
git add e2e/validator/validator.go e2e/validator/validator_test.go e2e/validator/routing_live_test.go scripts/e2e-routing.sh scripts/check.sh
git commit -m "feat(e2e): validate per-conversation affinity through the routing stack"
```

---

### Task 3: Kubernetes example manifests

**Files:**
- Create: `examples/k8s-ha/README.md`
- Create: `examples/k8s-ha/tier2-config.yaml`
- Create: `examples/k8s-ha/tier2.yaml`
- Create: `examples/k8s-ha/tier1-config.yaml`
- Create: `examples/k8s-ha/tier1.yaml`

**Interfaces:**
- Consumes: gateway/tier-2 config shapes proven by Tasks 1-2 (identical semantics; only resolver and exporters differ), spec sections "Required supporting changes" and "Remaining caveats".
- Produces: nothing later tasks depend on; Task 4 links here.

- [ ] **Step 1: Write the tier-2 ConfigMap**

`examples/k8s-ha/tier2-config.yaml` — the e2e tier-2 config with the e2e-only pieces removed (no `resource/e2e`, no file exporter) and an onward OTLP exporter (the only network exporter in this distribution; see `builder-config.yaml`):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: coding-agent-collector-config
  namespace: coding-agent-telemetry
data:
  config.yaml: |
    extensions:
      health_check:
        endpoint: 0.0.0.0:13133

    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318

    connectors:
      coding_agent:
        turn_timeout: 30s
        reorder_window: 2s
        max_active_turns: 100
        max_events_per_turn: 1000
      coding_agent/claude:

    processors:
      batch:
        timeout: 500ms

    exporters:
      # Point at your trace backend. This distribution ships the OTLP gRPC
      # exporter; for durable per-replica queues or S3 archival, adapt
      # examples/otelcol-s3.yaml instead.
      otlp:
        endpoint: ${env:CANONICAL_OTLP_ENDPOINT}

    service:
      extensions: [health_check]
      pipelines:
        logs/vendor:
          receivers: [otlp]
          processors: [batch]
          exporters: [coding_agent]
        traces/native:
          receivers: [otlp]
          processors: [batch]
          exporters: [coding_agent/claude]
        traces/canonical:
          receivers: [coding_agent, coding_agent/claude]
          processors: [batch]
          exporters: [otlp]
```

- [ ] **Step 2: Write the tier-2 workload**

`examples/k8s-ha/tier2.yaml` (Namespace + Deployment + headless Service; the headless Service is what the tier-1 k8s resolver watches):

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: coding-agent-telemetry
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coding-agent-collector
  namespace: coding-agent-telemetry
spec:
  replicas: 3
  selector:
    matchLabels:
      app: coding-agent-collector
  template:
    metadata:
      labels:
        app: coding-agent-collector
    spec:
      containers:
        - name: collector
          # This repository publishes no image. Build the root Dockerfile and
          # push it to your registry:
          #   docker build --tag <registry>/otelcol-coding-agents:<tag> .
          image: REPLACE_WITH_YOUR_REGISTRY/otelcol-coding-agents:latest
          args: ["--config=/etc/otelcol/config.yaml"]
          env:
            - name: CANONICAL_OTLP_ENDPOINT
              value: "REPLACE_WITH_YOUR_BACKEND:4317"
          ports:
            - containerPort: 4317
            - containerPort: 4318
            - containerPort: 13133
          readinessProbe:
            httpGet:
              path: /
              port: 13133
          livenessProbe:
            httpGet:
              path: /
              port: 13133
          volumeMounts:
            - name: config
              mountPath: /etc/otelcol
      volumes:
        - name: config
          configMap:
            name: coding-agent-collector-config
---
apiVersion: v1
kind: Service
metadata:
  name: coding-agent-collector
  namespace: coding-agent-telemetry
spec:
  clusterIP: None
  selector:
    app: coding-agent-collector
  ports:
    - name: otlp-grpc
      port: 4317
```

- [ ] **Step 3: Write the tier-1 ConfigMap**

`examples/k8s-ha/tier1-config.yaml` — the Task 1 gateway config with the k8s resolver in place of the static one:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: coding-agent-gateway-config
  namespace: coding-agent-telemetry
data:
  gateway.yaml: |
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318

    exporters:
      load_balancing:
        routing_key: attributes
        routing_attributes:
          - conversation.id
          - cursor.conversation.id
        protocol:
          otlp:
            tls:
              insecure: true
            retry_on_failure:
              enabled: true
            sending_queue:
              enabled: true
        resolver:
          k8s:
            service: coding-agent-collector.coding-agent-telemetry

    service:
      pipelines:
        logs:
          receivers: [otlp]
          exporters: [load_balancing]
        traces:
          receivers: [otlp]
          exporters: [load_balancing]
```

- [ ] **Step 4: Write the tier-1 workload and RBAC**

`examples/k8s-ha/tier1.yaml` (ServiceAccount + Role + RoleBinding + Deployment + ingest Service). The gateway needs the Role: without EndpointSlice access the k8s resolver's endpoint list stays empty and the gateway routes nothing (spec, "Required supporting changes"):

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: coding-agent-gateway
  namespace: coding-agent-telemetry
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: coding-agent-gateway-endpoints
  namespace: coding-agent-telemetry
rules:
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: coding-agent-gateway-endpoints
  namespace: coding-agent-telemetry
subjects:
  - kind: ServiceAccount
    name: coding-agent-gateway
    namespace: coding-agent-telemetry
roleRef:
  kind: Role
  name: coding-agent-gateway-endpoints
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coding-agent-gateway
  namespace: coding-agent-telemetry
spec:
  replicas: 2
  selector:
    matchLabels:
      app: coding-agent-gateway
  template:
    metadata:
      labels:
        app: coding-agent-gateway
    spec:
      serviceAccountName: coding-agent-gateway
      containers:
        - name: gateway
          image: otel/opentelemetry-collector-contrib:0.159.0
          args: ["--config=/etc/otelcol/gateway.yaml"]
          ports:
            - containerPort: 4317
            - containerPort: 4318
          volumeMounts:
            - name: config
              mountPath: /etc/otelcol
      volumes:
        - name: config
          configMap:
            name: coding-agent-gateway-config
---
apiVersion: v1
kind: Service
metadata:
  name: coding-agent-ingest
  namespace: coding-agent-telemetry
spec:
  selector:
    app: coding-agent-gateway
  ports:
    - name: otlp-grpc
      port: 4317
    - name: otlp-http
      port: 4318
```

- [ ] **Step 5: Write the example README**

`examples/k8s-ha/README.md` covering, tersely: the two-tier topology (agents → `coding-agent-ingest` → gateway → headless service → tier-2); prerequisites (build and push the root Dockerfile; a trace backend for `CANONICAL_OTLP_ENDPOINT`); apply order (`tier2-config.yaml`, `tier2.yaml`, `tier1-config.yaml`, `tier1.yaml`); and the operational caveats copied from the spec — topology changes remap roughly R/N routes and split any turn spanning the change; rolling restarts truncate active turns (`finish_reason=shutdown`); recovery = replay raw logs through one instance and dedupe on trace ID (spec, "Recovery property"). Point at `compose.e2e-routing.yaml` as the runnable twin of this topology and at `docs/multi-instance-ha.md` for the analysis.

- [ ] **Step 6: Check the manifests**

```bash
docker run --rm -v "$PWD/examples/k8s-ha:/manifests:ro" \
  ghcr.io/yannh/kubeconform:latest -summary /manifests
```

Expected: 0 invalid resources. (kubeconform reads only `*.yaml`; if it complains about the README, pass the four YAML files explicitly.)

- [ ] **Step 7: Commit**

```bash
git add examples/k8s-ha/README.md examples/k8s-ha/tier2-config.yaml examples/k8s-ha/tier2.yaml examples/k8s-ha/tier1-config.yaml examples/k8s-ha/tier1.yaml
git commit -m "docs(examples): add two-tier HA Kubernetes manifests"
```

---

### Task 4: Documentation updates

**Files:**
- Edit: `docs/multi-instance-ha.md` (status paragraph)
- Edit: `e2e/README.md` (routing stack section)
- Edit: `README.md` (deployment pointer in the references/deployment area)

**Interfaces:**
- Consumes: everything landed in Tasks 1-3 (paths and script names must match exactly).
- Produces: nothing; terminal task.

- [ ] **Step 1: Update the analysis doc**

In `docs/multi-instance-ha.md`, directly under the title, add a short status paragraph: Option B now has runnable artifacts — the routing e2e (`scripts/e2e-routing.sh`, `compose.e2e-routing.yaml`) proves the consistent-hash affinity with the pinned collector versions, and `examples/k8s-ha/` carries the Kubernetes shape with the k8s resolver and RBAC. The analysis below stays as the rationale record. Keep the rest of the document unchanged.

- [ ] **Step 2: Document the routing e2e**

In `e2e/README.md`, add a `## Routing E2E (zero API cost)` section after the existing stacks: what it builds (two tier-2 replicas + stock contrib gateway), what it replays (the pinned codex fixture as `CONVERSATIONS` synthetic conversations), what it asserts (per-conversation wholeness on exactly one replica; spread logged, not asserted), and how to run it (`GOTOOLCHAIN=auto ./scripts/e2e-routing.sh` — no credentials). Note that this stack is the runnable twin of `examples/k8s-ha/`.

- [ ] **Step 3: Point the README at the deployment example**

In the root `README.md`, near the `examples/otelcol-s3.yaml` mention, add one sentence linking `examples/k8s-ha/` for multi-instance deployment and `docs/multi-instance-ha.md` for the analysis.

- [ ] **Step 4: Run the full gate**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: ends `ALL CHECKS PASSED`.

- [ ] **Step 5: Commit**

```bash
git add docs/multi-instance-ha.md e2e/README.md README.md
git commit -m "docs: record Option B routing artifacts and how to run them"
```

---

## Plan self-review notes

- Spec coverage: attributes routing + both conversation keys (Tasks 1, 3); stock contrib tier 1 / unchanged tier 2 (Tasks 1, 3); k8s resolver + EndpointSlice RBAC (Task 3); deliberate queue/retry enablement (gateway configs, Tasks 1 and 3); remap and truncation caveats + raw-replay recovery documented where operators will read them (Task 3 README, Task 4); no connector code changes anywhere.
- Deliberately out of scope: publishing a container image (the repo has none; manifests carry a build-and-push note), Cursor traffic generation in the e2e (the routing key covers `cursor.conversation.id`, but no cursor fixture replay exists — the codex fixture exercises the identical mechanism), and a kind-based cluster test (kubeconform + the compose e2e cover config and semantics; a live cluster adds no assertion the compose stack does not already make).
- The one config-schema risk — the exact `load_balancing` sub-keys v0.159.0 accepts — falls to real binaries: Task 1 Step 5 checks both configs there, and the check.sh additions in Task 2 Step 8 keep enforcing that forever.
