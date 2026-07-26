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
├── connector/codingagentconnector/  # the connector component (own Go module)
│   ├── config.go, factory.go        #   public Collector component surface
│   ├── metadata.yaml, doc.go        #   mdatagen source and generate directive
│   ├── internal/codex/              #   stateful log correlation and trace building
│   └── internal/claude/             #   stateless native-span normalization
├── e2e/                             # real agent runners and OTLP JSON validator
│   └── responses-proxy/             #   Responses->Chat shim, e2e-only (see e2e/README.md)
├── examples/otelcol-s3.yaml         # S3 export with persistent local queues
├── builder-config.yaml              # pinned OCB distribution
├── compose.e2e-base.yaml            # shared collector for both e2e stacks
└── collector-config.yaml            # raw and canonical pipelines
```

The connector lives in its own module under `connector/codingagentconnector/`,
matching the layout of components in `opentelemetry-collector-contrib` so it can
be upstreamed with minimal changes.

## Status

The component is at development stability. Codex reconstruction state is in memory
and does not survive Collector restarts. See [the design document](docs/design.md)
for decisions, assumptions, and operational limits.

## Build

The connector component (`connector/codingagentconnector/`) and the e2e validator
(repo root) are separate Go modules, so run tests in both:

```bash
go test ./... && (cd connector/codingagentconnector && go test -race ./...)
```

Build the custom Collector container:

```bash
docker build -t otelcol-coding-agents:dev .
```

Or install OCB and generate the distribution directly:

```bash
builder --config builder-config.yaml
./dist/otelcol-coding-agents --config collector-config.yaml
```

Regenerate the component's mdatagen artifacts after editing `metadata.yaml`:

```bash
./scripts/generate.sh
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

The default suite (`go test`) covers event parsing and coercion, config
validation, deterministic trace construction/hierarchy/tokens/status/redaction,
bounded-state/out-of-order/timeout/shutdown/turn-splitting, factory/lifecycle,
Claude native-tree normalization, and the e2e trace-validation assertions in
`e2e/validator`.

The live, paid end-to-end tests (real Codex and Claude runs) are opt-in and
documented separately in [`e2e/README.md`](e2e/README.md).

## CI and releases

GitHub Actions runs formatting, shell syntax, lint, vet, unit/integration tests,
race tests, custom Collector builds, Collector/Compose validation, container
builds, and GoReleaser configuration checks. Live agent E2Es remain manual and
never receive credentials in CI.

Pushing a semantic version tag such as `v0.1.0` runs GoReleaser and creates a
GitHub release containing cross-platform custom Collector archives and SHA-256
checksums:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The same tag also publishes a `linux/amd64` container image to
`ghcr.io/nijave/otel-agent-trace-connector`, tagged exactly as the git tag
reads:

```bash
docker pull ghcr.io/nijave/otel-agent-trace-connector:v0.1.0
```

The image is the repository `Dockerfile` and carries the bundled
`collector-config.yaml` as its default `--config`; deployments that export
elsewhere should mount their own config over it.

## References

- [OpenTelemetry Collector Builder](https://opentelemetry.io/docs/collector/extend/ocb/)
- [OpenTelemetry Collector connectors](https://opentelemetry.io/docs/collector/components/connector/)
- [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
- [Codex observability and telemetry](https://developers.openai.com/codex/config-advanced#observability-and-telemetry)
- [Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)
- [Claude Code third-party endpoints](https://code.claude.com/docs/en/llm-gateway)
- [z.ai coding-agent setup](https://docs.z.ai/devpack/tool/claude)
- [AWS S3 exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/awss3exporter)
- [File storage extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/extension/storage/filestorage)
