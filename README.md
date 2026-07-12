# Coding-agent OpenTelemetry connector

This repository provides an external OpenTelemetry Collector connector and an
OCB-built distribution for coding-agent traces:

- **Codex:** correlates structured `codex.*` OTLP logs into one canonical trace
  per user turn.
- **Claude Code:** preserves its native span hierarchy and normalizes the
  interaction, LLM, and tool span names and attributes into the same canonical
  vocabulary.

The canonical tree is:

```text
invoke_agent <agent>
├── chat <model>
└── execute_tool <tool>
```

The raw vendor logs and traces are exported separately. Normalization never
copies prompt text, tool arguments, or tool output into generated Codex spans.

Repository layout:

```text
.
├── config.go, factory.go       # public Collector component surface
├── internal/codex/             # stateful log correlation and trace building
├── internal/claude/            # stateless native-span normalization
├── e2e/                        # real Codex runner and OTLP JSON validator
├── examples/otelcol-s3.yaml    # S3 export with persistent local queues
├── builder-config.yaml         # pinned OCB distribution
└── collector-config.yaml       # raw and canonical pipelines
```

## Status

The component is at development stability. It uses Collector v0.156.0 and Go
1.25. Codex reconstruction state is in memory and does not survive Collector
restarts. See [the design document](docs/design.md) for decisions, assumptions,
and operational limits.

## Build

Build and test the Go component:

```bash
go test ./...
go test -race ./...
```

If your normal Go module cache is read-only, use a writable cache:

```bash
GOMODCACHE=/tmp/otel-agent-trace-connector-gomodcache go test ./...
```

Build the custom Collector container:

```bash
docker build -t otelcol-coding-agents:dev .
```

Or install OCB v0.156.0 and generate the distribution directly:

```bash
builder --config builder-config.yaml
./dist/otelcol-coding-agents --config collector-config.yaml
```

## Collector configuration

The connector can bridge either logs or traces into a traces pipeline:

```yaml
connectors:
  coding_agent:
    turn_timeout: 10m
    reorder_window: 30s
    max_active_turns: 10000
    max_events_per_turn: 1000
  coding_agent/claude:

service:
  pipelines:
    logs/vendor:
      receivers: [otlp]
      exporters: [raw_logs, coding_agent]
    traces/native:
      receivers: [otlp]
      exporters: [raw_traces, coding_agent/claude]
    traces/canonical:
      receivers: [coding_agent, coding_agent/claude]
      exporters: [canonical]
```

`reorder_window` starts after the most recently received Codex event. A turn
with at least one `response.completed` event is finalized when that window is
quiet. A turn without completion is finalized by `turn_timeout`, shutdown, a
new prompt in the same conversation, or bounded-state eviction.

Codex telemetry must be configured in the user-level `config.toml`; Codex
ignores project-local `[otel]` configuration. Prompt logging should remain off.
See the [official Codex observability documentation](https://developers.openai.com/codex/config-advanced#observability-and-telemetry).

Claude Code should export its native beta traces directly. See the
[official Claude Code monitoring documentation](https://code.claude.com/docs/en/monitoring-usage#traces-beta).

### S3 reference deployment

[`examples/otelcol-s3.yaml`](examples/otelcol-s3.yaml) is a production-oriented
reference that stores raw logs, raw native traces, and canonical traces under
separate S3 prefixes. Each S3 exporter uses a bounded `sending_queue` backed by
the Collector file-storage extension, so completed batches waiting for S3 can
survive a Collector restart.

Build the distribution, provide a pre-existing bucket, and run the example:

```bash
builder --config builder-config.yaml
export OTEL_S3_BUCKET=my-telemetry-bucket
export AWS_REGION=us-east-1
export OTEL_QUEUE_DIRECTORY=/var/lib/otelcol/sending-queue
./dist/otelcol-coding-agents --config examples/otelcol-s3.yaml
```

Mount `OTEL_QUEUE_DIRECTORY` on durable local storage with sufficient capacity;
an ephemeral container filesystem defeats restart recovery. Each Collector
replica should have its own local volume. The default file-storage limit is 1
GiB per queue database and can be changed with
`OTEL_QUEUE_MAX_SIZE_BYTES`. Queue capacity is also bounded to 10,000 requests
per exporter.

The example intentionally contains no access keys. Supply credentials through
the standard AWS SDK credential chain, preferably with a workload, task, or
instance role granting only the required bucket access. `OTEL_S3_BASE_PREFIX`
defaults to `coding-agent-otel` and can be overridden.

The queues persist telemetry that has reached an exporter; they do not persist
the connector's active Codex turn-correlation state. Also account for the raw
telemetry privacy considerations below before enabling this configuration.

## Tests

The normal test suite includes:

- event parsing and type-coercion unit tests;
- configuration validation tests;
- deterministic trace construction, hierarchy, token, status, and redaction tests;
- bounded-state, out-of-order, timeout/shutdown, and turn-splitting tests;
- Collector factory/lifecycle integration tests;
- Claude native-tree normalization and non-mutation tests;
- the E2E output validator as a separately compiled Go command.

The live E2E is intentionally separate because it calls a real model and may
incur API cost.

## Live Codex E2E

The Compose test builds the custom Collector, launches a real non-interactive
Codex session which must use one harmless shell tool, waits for trace
reconstruction, and validates the exported OTLP JSON. It requires a unique run
ID and checks the root/child hierarchy, canonical attributes, completion state,
and absence of sensitive copied fields.

Prerequisites:

- Docker with Compose v2;
- an `OPENAI_API_KEY` authorized for Codex/API usage;
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export OPENAI_API_KEY=...
./scripts/e2e.sh
```

Optional overrides:

```bash
CODEX_VERSION=0.144.1 E2E_CODEX_MODEL=gpt-5.1-codex-mini E2E_AGENT_TIMEOUT=10m ./scripts/e2e.sh
```

The E2E defaults to `gpt-5.1-codex-mini`, the smaller, lower-cost Codex model,
and mounts the host CA bundle read-only so managed/private certificate
authorities remain trusted inside the Codex container. On hosts where the CA
bundle is elsewhere, set `E2E_CA_BUNDLE` to its absolute path. TLS verification
remains enabled. The runner authenticates through Codex's noninteractive
API-key login; its credential store exists only inside the ephemeral runner
container.

The script writes raw logs, raw native traces, and canonical traces under
`.e2e-output/`. To inspect or rerun Compose manually:

```bash
export E2E_RUN_ID="manual-$(date +%s)"
docker compose build
docker compose up --detach --wait collector
docker compose run --rm --no-deps codex
docker compose run --rm --no-deps validator
docker compose down
```

The repository's automated verification does not run this paid test.

## References

- [OpenTelemetry Collector Builder](https://opentelemetry.io/docs/collector/extend/ocb/)
- [OpenTelemetry Collector connectors](https://opentelemetry.io/docs/collector/components/connector/)
- [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
- [Codex observability and telemetry](https://developers.openai.com/codex/config-advanced#observability-and-telemetry)
- [Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)
- [AWS S3 exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.156.0/exporter/awss3exporter)
- [File storage extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.156.0/extension/storage/filestorage)
