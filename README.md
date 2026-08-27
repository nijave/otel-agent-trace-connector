# Coding-agent OpenTelemetry connector

An organization adopting AI coding agents receives telemetry in as many
shapes as it runs agents. This connector normalizes those shapes into one
canonical trace vocabulary so the same queries measure token usage — and
the cost derived from it — across every agent. Latency and tool
activity ride the same spans. Prompt text, tool arguments, and tool output
never enter canonical output, so usage visibility does not require
collecting what developers type. [docs/design.md](docs/design.md) records
the use case and design goals.

This repository provides an external OpenTelemetry Collector connector and an
OCB-built distribution for coding-agent traces:

- **Codex:** correlates structured `codex.*` OTLP logs into one canonical trace
  per user turn.
- **Cursor:** correlates native `cursor.telemetry` OTLP logs (Enterprise
  beta, metrics + logs only) into one canonical trace per activity burst,
  keyed on `cursor.conversation.id`. Chat spans carry per-request token
  usage; the wire reports tool calls only as metrics without correlation
  IDs, so canonical traces have no `execute_tool` children.
- **OpenCode:** renames its native Vercel AI SDK spans (`ai.streamText`,
  `ai.streamText.doStream`, `ai.toolCall`) into one `invoke_agent opencode`
  canonical trace per model step, dropping internal instrumentation spans and
  all content attributes.
- **GitHub Copilot:** normalizes native GenAI-semconv traces from Copilot CLI
  and VS Code Chat (`invoke_agent`/`chat`/`execute_tool`) through the shared
  GenAI-semconv edge
- **Claude Code:** preserves its native span hierarchy and normalizes the
  interaction, LLM, and tool span names and attributes into the same canonical
  vocabulary.
- **GenAI semconv sources:** normalizes native traces from
  `opentelemetry-instrumentation-openai-v2` (both semconv modes),
  direct `opentelemetry-util-genai` users, and the Strands Agents SDK into
  the same canonical vocabulary, stripping prompt/completion/tool content
  from canonical output.
- **Pi:** normalizes native traces emitted by the
  [`@amaster.ai/pi-telemetry`](https://www.npmjs.com/package/@amaster.ai/pi-telemetry)
  extension for the [Pi coding agent](https://pi.dev). Each user input becomes
  one `invoke_agent pi` root per agentic iteration, with `chat <model>` and
  `execute_tool <tool>` children; exporter-local Langfuse baggage and raw
  usage blobs never reach canonical output.
- **OpenHands:** normalizes native OpenTelemetry traces from the OpenHands
  SDK (Laminar instrumentation, scope `lmnr.tracer`) into one canonical
  `invoke_agent openhands` trace per conversation, keyed on the SDK's
  session id. Delegate subagents arrive as sibling traces sharing the
  conversation id. Streamed completions carry no token usage upstream.
  Enable export with `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` plus
  `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf`.

The canonical tree is:

```text
invoke_agent <agent>
├── chat <model>
└── execute_tool <tool>
```

The raw vendor logs and traces export separately. Normalization never
copies prompt text, tool arguments, or tool output into canonical output,
on any edge.

Canonical output uses a closed attribute vocabulary documented in
[docs/canonical-attributes.md](docs/canonical-attributes.md), with a raw →
canonical mapping matrix per harness under
[docs/harnesses/](docs/harnesses/) and the upstream research record in
[docs/harnesses.md](docs/harnesses.md).

## Supported harnesses

These are the sources the connector sorts today, and what each needs. The
logs edge (`coding_agent` on a logs pipeline) correlates structured logs into
canonical traces. The traces edge (`coding_agent/claude` on a traces
pipeline) normalizes native traces in place. Detection within an edge is
automatic. No per-source connector setting exists.

| Harness | Edge | Connector detection | Harness-side configuration |
| --- | --- | --- | --- |
| Codex | logs | log records whose `event.name` starts with `codex.` | `[otel]` block in user-level `~/.codex/config.toml` |
| Cursor | logs | instrumentation scope starting with `cursor.telemetry` | Team Settings → OpenTelemetry Export (Enterprise beta), OTLP/HTTP to `/v1/logs` |
| Claude Code | traces | span names starting with `claude_code.` | telemetry env vars with beta trace flag (below) |
| OpenCode | traces | instrumentation scope named exactly `opencode` | `experimental.openTelemetry: true` plus OTLP endpoint env vars |
| GitHub Copilot | traces | instrumentation scope starting with `github.copilot` (GenAI-semconv edge) | `COPILOT_OTEL_ENABLED=true` or set `OTEL_EXPORTER_OTLP_ENDPOINT`; OTLP/HTTP or file exporter (`COPILOT_OTEL_FILE_EXPORTER_PATH`) |
| openai-v2 / util-genai agents | traces | instrumentation scope starting with `opentelemetry.instrumentation.openai_v2` or `opentelemetry.util.genai` | standard OpenTelemetry SDK env vars |
| Strands Agents SDK | traces | instrumentation scope starting with `strands.telemetry` | standard OpenTelemetry SDK env vars |
| Pi | traces | instrumentation scope starting with `@amaster.ai/pi-telemetry`, or resource `telemetry.sdk.name` with the same prefix | install the `@amaster.ai/pi-telemetry` extension and enable its exporter (below) |
| OpenHands | traces | instrumentation scope `lmnr.tracer` whose spans carry OpenHands marker span names or the `lmnr.association.properties.metadata.is_delegate=true` flag | `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` plus `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf` |

Minimal harness-side settings, as exercised by the e2e stacks:

Codex reads `[otel]` only from the user-level `~/.codex/config.toml` and
ignores project-local `[otel]` tables:

```toml
[otel]
log_user_prompt = false
exporter = { otlp-http = { endpoint = "http://collector:4318/v1/logs", protocol = "binary" } }
```

Cursor needs no client setting. An admin points Team Settings → OpenTelemetry
Export at the Collector over OTLP/HTTP binary protobuf. Metrics and logs
arrive server-side. The connector correlates the logs.

Claude Code exports its native beta traces when these variables reach its
process:

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://collector:4318/v1/traces
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf
```

Leave Claude Code's content gates unset so prompt text, tool arguments, tool
output, and raw API bodies stay out of the raw destination.

OpenCode enables its tracer with `"experimental": {"openTelemetry": true}` in
`opencode.json` and exports wherever `OTEL_EXPORTER_OTLP_ENDPOINT` points
(set `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`). Releases since
`opencode-ai` 1.18.21 ship the native tracer; older releases emit nothing
under the flag alone.

Pi has no native OTel surface. Install the
[`@amaster.ai/pi-telemetry`](https://www.npmjs.com/package/@amaster.ai/pi-telemetry)
extension (`pi install npm:@amaster.ai/pi-telemetry`, which registers itself
under `"packages"` in `~/.pi/agent/settings.json`) and enable its exporter in
the same file:

```json
{
  "pi-telemetry": {
    "serviceName": "pi",
    "otel": { "enabled": true, "endpoint": "http://collector:4318/v1/traces" }
  }
}
```

GenAI-semconv sources need no keys beyond the standard SDK environment
(`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`).

No support yet: Cline, Kilo Code CLI, Hermes Agent, and hook- or
plugin-based surfaces such as Cursor hooks, other Pi extensions, and OpenCode
plugins. [docs/harnesses.md](docs/harnesses.md) records what each exports and
why it does not fit today.

Repository layout:

```text
.
├── connector/codingagentconnector/  # the connector component (own Go module)
│   ├── config.go, factory.go        #   public Collector component surface
│   ├── metadata.yaml, doc.go        #   mdatagen source and generate directive
│   ├── internal/codex/              #   stateful log correlation and trace building
│   ├── internal/cursor/             #   burst correlation of native Cursor logs
│   ├── internal/claude/             #   stateless native-span normalization
│   ├── internal/genai/, opencode/,  #   more stateless traces-edge normalizers
│   │   openhands/
│   └── internal/pi/                 #   stateless @amaster.ai/pi-telemetry normalization
├── e2e/                             # real agent runners and OTLP JSON validator
│   └── responses-proxy/             #   Responses->Chat shim, e2e-only (see e2e/README.md)
├── examples/otelcol-s3.yaml         # S3 export with persistent local queues
├── builder-config.yaml              # pinned OCB distribution
├── compose.e2e-base.yaml            # shared collector included by every e2e stack
└── collector-config.yaml            # raw and canonical pipelines
```

The connector lives in its own module under `connector/codingagentconnector/`,
matching the layout of components in `opentelemetry-collector-contrib` and upstreams with minimal changes.

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
with at least one `response.completed` event finalizes when that window is
quiet. A turn without completion finalizes by `turn_timeout`, shutdown, a
new prompt in the same conversation, or bounded-state eviction.

One `coding_agent` instance on the logs pipeline claims both log sources:
Codex records by their `codex.`-prefixed event names and Cursor records by
their `cursor.telemetry` instrumentation scope. Cursor has no prompt or
completion event on the wire, so each conversation's activity burst finalizes
when `reorder_window` passes with no new record, by `turn_timeout`, at
shutdown, or through bounded-state eviction.

The traces edge auto-detects Claude Code, OpenCode, OpenHands, and
GenAI-semconv sources, so every source enters the same pipeline.

Strands captures prompt and completion content in span events by default and
its redaction is opt-in, so the raw trace destination receives content under
default agent settings. Configure Strands redaction
(`gen_ai_unredacted_attributes` in `OTEL_SEMCONV_STABILITY_OPT_IN`) or apply
the same access policy to the raw destination and any content store. The
canonical pipeline strips content-bearing attributes and events regardless.

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

Mount `OTEL_QUEUE_DIRECTORY` on durable local storage with enough capacity;
an ephemeral container filesystem defeats restart recovery. Each Collector
replica should have its own local volume. The default file-storage limit is 1
GiB per queue database and changes with
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
Claude native-tree normalization, GenAI semconv normalization, and the e2e
trace-validation assertions in `e2e/validator`.

The live, paid end-to-end tests (real Codex and Claude runs) are opt-in and
documented separately in [`e2e/README.md`](e2e/README.md). More opt-in
stacks exercise the remaining harnesses — the openai-v2 ad-hoc agent,
Strands, OpenCode, Pi, Copilot CLI, and OpenHands — and every stack shares
the same collector base.

## CI and releases

GitHub Actions runs formatting, shell syntax, lint, vet, unit/integration tests,
race tests, custom Collector builds, Collector/Compose validation, container
builds, and GoReleaser configuration checks. Live agent E2Es remain manual and
never receive credentials in CI.

Run the same unpaid checks locally before pushing:

```bash
./scripts/check.sh
```

The script needs `go`, `golangci-lint` v2.13.1 (the version CI pins; install
with `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1`),
`shellcheck`, `jq`, `docker`, and `goreleaser`. It covers gofmt, shell syntax
and shellcheck, golangci-lint on both modules, mdatagen freshness, vet, tests
and race tests in both modules, Collector build and config validation, Compose
validation including the credential-split assertions, container image builds,
and `goreleaser check` — the full unpaid CI surface.

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
- [Cursor OpenTelemetry Export](https://cursor.com/docs/enterprise/opentelemetry-export)
- [Cursor OpenTelemetry wire reference](https://cursor.com/docs/enterprise/opentelemetry-export/wire)
- [Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)
- [Claude Code third-party endpoints](https://code.claude.com/docs/en/llm-gateway)
- [z.ai coding-agent setup](https://docs.z.ai/devpack/tool/claude)
- [AWS S3 exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/awss3exporter)
- [File storage extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/extension/storage/filestorage)
- [opentelemetry-instrumentation-openai-v2](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai/opentelemetry-instrumentation-openai-v2)
- [Strands Agents traces](https://strandsagents.com/docs/user-guide/observability-evaluation/traces/)
- [OpenHands SDK observability](https://docs.openhands.dev/sdk/guides/observability)
