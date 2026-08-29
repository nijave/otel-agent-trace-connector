# Pi — OpenTelemetry signal reference

Pi (pi.dev) has no built-in OTel exporter of its own — every signal it can
emit comes from third-party npm extensions installed into
`~/.pi/agent/extensions/`, and each extension picked its own subset of
signals to support. This file's attribute matrix covers exactly one of them:
`@amaster.ai/pi-telemetry`, the only extension the connector claims spans
from. See [canonical attributes](../canonical-attributes.md) for the shared
vocabulary and the policy behind it, and
[docs/otel-signals.md](../otel-signals.md) for the full per-extension survey
this file draws on.

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | via extension (`@amaster.ai/pi-telemetry` and 4 others) | traces edge (this file's Connector mapping, `@amaster.ai/pi-telemetry` only) |
| Logs | via extension (`pi-otel` only, of extensions surveyed) | not consumed |
| Metrics | via extension (`pi-otel-telemetry`, `devkade/pi-opentelemetry`) | not applicable |

Last verified: 2026-08-29 (Logs, Metrics rows); 2026-08-27 (Traces row, this
file's attribute matrix). See Sources below.

## Traces

### Native support

`@amaster.ai/pi-telemetry` emits OTLP traces whose instrumentation scope is
its package name (with `telemetry.sdk.name` set to match on the resource).
Turn spans arrive as `chat-turn`, generations as `llm-generation …`, and
tools as spans named after the bare tool with the identity in attributes.
Sibling extensions also provide traces — `pi-otel-telemetry`,
`@the-agency/pi-observability`, `pi-otel` (NikiforovAll), `maxmalkin/pi-OTEL`
— each with its own span shape; see
[docs/otel-signals.md](../otel-signals.md#pi) for the full roster. None of
them are native to Pi itself.

### Connector mapping

The connector claims any group carrying the `@amaster.ai/pi-telemetry` scope
or matching `sdk.name` and rewrites each trace as an `invoke_agent pi` root
with reparented `chat <model>` and `execute_tool <tool>` children. Non-native
spans in a claimed group (sibling instrumentation scopes swept in by the
process-wide claim) drop from canonical output; the raw pipelines preserve
the originals.

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

##### llm-generation span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `model` | `gen_ai.request.model` | mapped (also names the span; a generation without `model` emits as bare `chat` with no key) |
| `provider` | `gen_ai.provider.name` | mapped (verbatim, when non-empty; deliberate divergence — the value is operator-authored and often a gateway/harness label, unlike the recognized-provider values other edges emit) |
| `usage.input` | `gen_ai.usage.input_tokens` | mapped |
| `usage.output` | `gen_ai.usage.output_tokens` | mapped |
| `usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped |
| `usage.cache_read` | `gen_ai.usage.cache_read.input_tokens` | mapped (the extension reports no-cache as `0`, not by omission) |
| `usage.cache_write` | `gen_ai.usage.cache_write.input_tokens` | mapped (same zero-not-absent behavior) |
| `stopReason` | `gen_ai.response.finish_reasons` | mapped (single-element slice, when non-empty; yields to a pre-existing canonical value) |
| `responseId` | `gen_ai.response.id` | mapped (when non-empty) |
| `usage`, `usage.cost.total` | — | dropped (deliberate: cost has no canonical counterpart) |
| `llmGenerationId` | — | dropped |
| `sessionId` | — | dropped (the turn root carries the conversation id — but only when the `chat-turn` ships in the same batch; see below) |
| `status` | — | dropped |
| any per-request duration field | — | not provided (no TTFT source on generation spans) |

The edge removes the flat `usage.*` keys per-span after mapping and drops
the serialized `usage` object (which additionally carries cost detail) whole.
Provider values may carry harness/route labels (for example a router or
gateway tag); the edge passes them through verbatim rather than normalizing
them.

##### chat-turn span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `sessionId` | `gen_ai.conversation.id` | mapped (`chat-turn` spans only; Pi exports children in batches without their turn, and such a batch emits `chat`/`execute_tool` spans with no conversation id anywhere — the `sessionId` they carry strips) |
| `eventType` | (`coding_agent.source.event`) | dropped (the connector pins the event name to `chat-turn`) |
| `durationMs` | — | dropped (whole-turn duration, not a TTFT source) |

Because `gen_ai.response.time_to_first_chunk` needs a first-chunk latency and
the wire only carries whole-turn durations, the key stays **not provided**
for this edge.

##### tool span → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `toolName` | `gen_ai.tool.name` | mapped (also the discriminator: only spans carrying `toolName` become `execute_tool`) |
| `toolCallId` | `gen_ai.tool.call.id` | mapped (when non-empty; `toolName` alone still produces the span) |
| `status` | — | dropped |
| `sessionId` | — | dropped (see the `chat-turn` row for the split-batch consequence) |

#### Connector-written attributes

The connector writes these itself rather than remapping them from raw keys.
They appear on every span of the three recognized shapes (`chat-turn`,
`llm-generation…` prefixes, and tool spans carrying `toolName`); a span
matching none of these shapes is not native Pi and drops from canonical
output (the raw pipelines preserve it):

- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `pi` (invoke_agent root only)
- `coding_agent.source` = `native`
- `coding_agent.client.name` = `pi`
- `coding_agent.client.version` (from the resource's `service.version` when
  present — the pinned capture's resource carries none, so no span from it
  has this key)
- `coding_agent.source.event` (original native span name for turns and
  tools; the constant `llm-generation` for generation spans, so the lane and
  index suffix drops)

Langfuse observation baggage (`langfuse.*`) is exporter-local metadata; the
edge strips it from every native span.

#### Canonical keys with no Pi source

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (only whole-turn durations exist upstream) |
| `gen_ai.usage.reasoning.output_tokens` | not provided |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `gen_ai.response.model` | not provided |
| `server.address` / `server.port` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `exception.*` | not provided |

## Logs

### Native support

Pi has no built-in logs signal. Of six Pi OTel extensions surveyed, only
**`pi-otel`** (NikiforovAll) emits a genuine OTLP logs signal: set
`PI_OTEL_LOGS=1` and it exports real OTLP LogRecords —
`pi.session.start`/`pi.session.end` at INFO, `pi.tool.error`/
`pi.llm_request.error` at ERROR — plus `@opentelemetry/diag` SDK-internal
bridging. `@amaster.ai/pi-telemetry` (the extension the connector claims
spans from) has no logs signal: its `dist/otel.d.ts` exposes only `/otel`
(traces) and `/langfuse` subpaths. `pi-otel-telemetry`,
`@the-agency/pi-observability`, and `maxmalkin/pi-OTEL` have no logs signal
either. `devkade/pi-opentelemetry` also has none (metrics and traces only).
A bonus find outside this survey, `senad-d/ObservMe`, emits genuine OTLP logs
alongside traces and metrics — the only extension found covering all three
signals — but no one has researched it for token usage or repo identity.
Source: https://nikiforovall.blog/pi-otel/configuration.

### Connector mapping

Not consumed. The connector claims spans from `@amaster.ai/pi-telemetry`,
which has no logs signal to begin with; `pi-otel`'s logs live in a different,
unrelated extension, so this signal isn't integrated even though it exists
somewhere in the Pi extension ecosystem.

## Metrics

### Native support

Via extension only:

- **`pi-otel-telemetry`** (mprokopov, auto-installed): 8 instruments, no
  `llm.model` label by design ("no model label to avoid series
  fragmentation" — source comment). Common attrs on every metric:
  `user.name`, `host.name`, `environment` (only if
  `OTEL_RESOURCE_ATTRIBUTES` sets it).

  | Metric | Type | Labels |
  | --- | --- | --- |
  | `pi.tokens.input` | Counter | common attrs |
  | `pi.tokens.output` | Counter | common attrs |
  | `pi.tool.calls` | Counter | `tool.name` + common |
  | `pi.tool.errors` | Counter | `tool.name` + common (only when `isError`) |
  | `pi.tool.duration` | Histogram (ms) | `tool.name` + common |
  | `pi.turns` | Counter | common attrs |
  | `pi.prompts` | Counter | common attrs |
  | `pi.session.duration` | Histogram (s) | common attrs |

- **`devkade/pi-opentelemetry`**: metrics confirmed at source level
  (`src/metrics/collector.ts` / `src/metrics/provider.ts` build a real
  `MeterProvider` + `OTLPMetricExporter`), defining counters
  `pi.session.count`, `pi.turn.count`, `pi.tool_call.count`,
  `pi.tool_result.count`, `pi.prompt.count`, `pi.token.usage`,
  `pi.cost.usage`, and histograms `pi.session.duration`, `pi.turn.duration`,
  `pi.tool.duration`.
- **`@amaster.ai/pi-telemetry`** (the connector-supported extension): no
  metrics.
- `@the-agency/pi-observability` and `pi-otel` (NikiforovAll) are
  traces/logs-focused; metrics not verified for either.

Full detail: [docs/metrics.md](../metrics.md#pi).

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (see
`connector/codingagentconnector/factory.go`); it does not process,
transform, or filter OTel metrics for any harness. This is also moot for Pi
specifically, since the extension the connector claims spans from exports no
metrics at all.

## Sources

- https://www.npmjs.com/package/@amaster.ai/pi-telemetry — the connector's
  traces source extension; `dist/otel.d.ts` shows no logs or metrics signal
  (refreshed 2026-08-29)
- https://github.com/mprokopov/pi-otel-telemetry — traces plus 8 metrics
  instruments, no logs (refreshed 2026-08-29)
- https://nikiforovall.blog/pi-otel/configuration — `pi-otel` (NikiforovAll)
  logs config, the only surveyed extension with genuine OTLP logs (refreshed
  2026-08-29)
- https://github.com/senad-d/ObservMe — bonus find covering all three
  signals; not yet researched for token usage or repo identity (refreshed
  2026-08-29)
- `devkade/pi-opentelemetry` source (`src/metrics/collector.ts`,
  `src/metrics/provider.ts`) — confirms real metrics instruments (refreshed
  2026-08-29)
- [docs/otel-signals.md](../otel-signals.md) — the full per-extension
  traces/logs/metrics survey this file draws on (refreshed 2026-08-29)
- [docs/metrics.md](../metrics.md) — Pi instrument catalog (refreshed
  2026-08-21)
- This file's Traces attribute matrix — last verified 2026-08-27
