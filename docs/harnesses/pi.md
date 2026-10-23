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

Pi's own repository does ship a `@earendil-works/pi-telemetry` package
(since 0.84.0), but it is a vendor-neutral span contract with "no exporter,
global current-span state, or dependency on a telemetry backend"; nothing
reaches an OTLP endpoint without an extension
([packages/telemetry/README.md at v1.3.0](https://github.com/earendil-works/pi/blob/v1.3.0/packages/telemetry/README.md),
[coding-agent CHANGELOG 0.84.0](https://github.com/earendil-works/pi/blob/v1.3.0/packages/coding-agent/CHANGELOG.md),
checked 2026-10-19).

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | via extension (`@amaster.ai/pi-telemetry` and the 5 others surveyed) | traces edge (this file's Connector mapping, `@amaster.ai/pi-telemetry` only) |
| Logs | via extension (`pi-otel` only, of extensions surveyed) | not consumed |
| Metrics | via extension (`pi-otel-telemetry`, `pi-otel`, `maxmalkin/pi-OTEL`, `devkade/pi-opentelemetry`) | not applicable |

Last verified: 2026-10-19 (all rows; extension package metadata and source,
plus live captures of `@amaster.ai/pi-telemetry` 0.1.9 under pi 0.84.2 and
0.1.28 under pi 1.3.0 for this file's attribute matrix). See Sources below.

## Traces

### Native support

`@amaster.ai/pi-telemetry` emits OTLP traces whose instrumentation scope is
its package name. The 0.1.9 capture also set the resource's
`telemetry.sdk.name` to the package name; the 0.1.28 capture carries the
OpenTelemetry SDK default `opentelemetry` there, so only the scope
identifies it (live capture, 2026-10-19).
Turn spans arrive as `chat-turn`, generations as `llm-generation …`, and
tools as spans named after the bare tool with the identity in attributes.
Release 0.1.28 also emits `llm-stream` spans (model output content) and moved
token accounting into one serialized `usage` object; both wires are covered
below. Sibling extensions also provide traces — `pi-otel-telemetry`,
`@the-agency/pi-observability`, `pi-otel` (NikiforovAll), `maxmalkin/pi-OTEL`,
`devkade/pi-opentelemetry` — each with its own span shape; see
[docs/otel-signals.md](../otel-signals.md#pi) for the full roster. None of
them are native to Pi itself.

### Connector mapping

The connector claims any group carrying the `@amaster.ai/pi-telemetry` scope
or matching `sdk.name` and rewrites each trace as an `invoke_agent pi` root
with reparented `chat <model>` and `execute_tool <tool>` children. Non-native
spans in a claimed group (sibling instrumentation scopes swept in by the
process-wide claim, and the extension's own `llm-stream` spans) drop from
canonical output; the raw pipelines preserve the originals.

Every native span rebuilds its attribute map from scratch: the canonical keys
below plus the connector-written set are written into a fresh map that
replaces the raw one. The mapping is an allowlist by construction, so a raw
key this file does not name — including whatever a future extension release
adds — cannot reach canonical output.

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there). The 0.1.9 capture carries flat `usage.*`
attributes and a `provider` attribute; 0.1.28 replaced both (usage now lives
only in one serialized object, provider only inside the model id).

##### llm-generation span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `requestedModel` | `gen_ai.request.model` | mapped (also names the span; absent when the extension omits it — the 0.1.9 wire — and `model` then supplies both) |
| `model` | `gen_ai.request.model` | mapped (only as the fallback when `requestedModel` is absent; on 0.1.28 the value is gateway-prefixed and names the span only through `requestedModel`) |
| `provider` | `gen_ai.provider.name` | mapped (verbatim, when non-empty — 0.1.9 wire only; deliberate divergence, the value is operator-authored and often a gateway/harness label, unlike the recognized-provider values other edges emit) |
| `model` of the exact shape `<prefix>/<requestedModel>` | `gen_ai.provider.name` | mapped (the prefix, for example `opencode-go`; absent when `provider` is present, when `model` carries no `/`, when `requestedModel` is absent, or when the remainder differs from `requestedModel` — the edge never guesses a provider) |
| `thinkingLevel` | `gen_ai.request.reasoning.level` | mapped (verbatim, when non-empty — 0.1.28 wire only) |
| `usage` → `input` | `gen_ai.usage.input_tokens` | mapped (serialized object, 0.1.28) |
| `usage` → `output` | `gen_ai.usage.output_tokens` | mapped (serialized object, 0.1.28) |
| `usage` → `totalTokens` | `gen_ai.usage.total_tokens` | mapped (serialized object, 0.1.28) |
| `usage` → `cacheRead` | `gen_ai.usage.cache_read.input_tokens` | mapped (serialized object, 0.1.28; the extension reports no-cache as `0`, not by omission) |
| `usage` → `cacheWrite` | `gen_ai.usage.cache_write.input_tokens` | mapped (serialized object, 0.1.28; same zero-not-absent behavior) |
| `usage` → `reasoning` | `gen_ai.usage.reasoning.output_tokens` | mapped (serialized object, 0.1.28 only; no flat counterpart exists) |
| `usage.input`, `usage.output`, `usage.total_tokens`, `usage.cache_read`, `usage.cache_write` | the same five canonical usage keys | mapped (flat attributes, 0.1.9; each field falls back to its flat key when the `usage` object omits it, and an unparsable object leaves the flat keys in charge) |
| `stopReason` | `gen_ai.response.finish_reasons` | mapped (single-element slice, when non-empty) |
| `responseId` | `gen_ai.response.id` | mapped (when non-empty) |
| `langfuse.observation.completion_start_time` | `gen_ai.response.time_to_first_chunk` | mapped (0.1.28; ISO-8601 timestamp of the first streamed token minus the span start, in seconds as a double; absent when the extension observed no first token, when the value does not parse as RFC 3339, or when it precedes the span start) |
| `usage` → `cost` → `total` | `coding_agent.usage.cost` | mapped (USD double, 0.1.28 serialized object; absent when the object has no numeric `cost.total`; the extension prices from its own model table, so the value is an estimate) |
| `usage.cost.total` | `coding_agent.usage.cost` | mapped (0.1.9 flat key, fallback only when the serialized object carries no `cost.total`) |
| `usage` → `cost` → `input`/`output`/`cacheRead`/`cacheWrite`, `usage` → `cacheWrite1h` | — | dropped (per-component cost breakdown and the 1h cache-write counter have no canonical counterpart) |
| `reasoning`, `cacheWrite1h` (standalone attributes) | — | dropped (0.1.28 duplicates the same numbers inside the `usage` object, which is the source the edge reads) |
| `llmGenerationId` | — | dropped |
| `sessionId` | — | dropped (the turn root carries the conversation id — but only when the `chat-turn` ships in the same batch; see below) |
| `status`, `api`, `source`, `costSource` | — | dropped |

Provider values may carry harness/route labels (for example a router or
gateway tag); the edge passes an explicit `provider` through verbatim rather
than normalizing it, and derives one from `model` only in the exact
`<prefix>/<requestedModel>` case above.

##### chat-turn span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `sessionId` | `gen_ai.conversation.id` | mapped (`chat-turn` spans only; Pi exports children in batches without their turn, and such a batch emits `chat`/`execute_tool` spans with no conversation id anywhere — the `sessionId` they carry drops) |
| `eventType` | (`coding_agent.source.event`) | dropped (the connector pins the event name to `chat-turn`) |
| `promptNumber` | — | dropped (0.1.28 prompt counter; no canonical counterpart) |
| `durationMs` | — | dropped (whole-turn duration, not a TTFT source) |

`durationMs` is a whole-turn duration, not a first-chunk latency; the TTFT
comes from the generation span's `completion_start_time` (mapped, see the
llm-generation table).

##### tool span → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `toolName` | `gen_ai.tool.name` | mapped (also the discriminator: only spans carrying `toolName` become `execute_tool`) |
| `toolCallId` | `gen_ai.tool.call.id` | mapped (when non-empty; `toolName` alone still produces the span) |
| `status` | — | dropped |
| `sessionId` | — | dropped (see the `chat-turn` row for the split-batch consequence) |
| `durationMs` | — | dropped (0.1.28 tool duration; no canonical counterpart) |

##### llm-stream span → dropped

0.1.28 adds `llm-stream` spans that carry the model output content in
`output.value` and `langfuse.observation.output`. `llm-stream` is not a
canonical span type, so the whole span drops along with that content; no
canonical key maps from it.

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
  present — both pinned captures' resources carry none, so no span from them
  has this key; 0.1.28 sets it only from the operator's
  `TELEMETRY_SERVICE_VERSION` or `OTEL_RESOURCE_ATTRIBUTES`, so it is not
  Pi's own version — `@amaster.ai/pi-telemetry@0.1.28` `dist/otel.js`,
  `dist/langfuse/exporters.js`, checked 2026-10-19)
- `coding_agent.source.event` (original native span name for turns and
  tools; the constant `llm-generation` for generation spans, so the lane and
  index suffix drops)

Because the canonical attribute map is rebuilt rather than filtered, no raw
key survives on a kept span — Langfuse observation baggage (`langfuse.*`),
the serialized `usage` object, cost detail, and diagnostic `status` fields all
drop without a denylist entry naming them.

#### Canonical keys with no Pi source

| Canonical key | Status |
|---|---|
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided (0.1.28 carries `max_tokens` only inside Langfuse model-parameter bags, which the edge does not parse) |
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
bridging (`pi-otel@0.3.0` `dist/index.js`, `dist/otel/logs.js`, checked
2026-10-19). `@amaster.ai/pi-telemetry` (the extension the connector claims
spans from) has no logs signal: 0.1.28 depends only on the OTLP trace
exporter and trace SDK, with no logs or metrics SDK (`package.json`, checked
2026-10-19). `pi-otel-telemetry`, `@the-agency/pi-observability`,
`maxmalkin/pi-OTEL`, and `devkade/pi-opentelemetry` have no logs signal
either (none depends on an OTLP logs exporter). A bonus find outside this
survey, `senad-d/ObservMe`, emits OTLP logs alongside traces and metrics
(it depends on the OTLP logs, metrics, and trace exporters at
[39c2b1f](https://github.com/senad-d/ObservMe/blob/39c2b1f71faef63b11909f5b0385b97d16cee6e1/package.json));
it and `pi-otel` are the two extensions found covering all three signals.
No one has researched ObservMe for token usage or repo identity.
Source: https://nikiforovall.blog/pi-otel/configuration.

### Connector mapping

Not consumed. The connector claims spans from `@amaster.ai/pi-telemetry`,
which has no logs signal to begin with; `pi-otel`'s logs live in a different,
unrelated extension, so this signal isn't integrated even though it exists
somewhere in the Pi extension ecosystem.

## Metrics

### Native support

Via extension only:

- **`pi-otel-telemetry`** (mprokopov): 8 instruments, no
  `llm.model` label by design ("no model label to avoid series
  fragmentation" — source comment). Common attrs on every metric:
  `user.name`, `host.name`, `environment` (only if
  `OTEL_RESOURCE_ATTRIBUTES` sets it). The common attrs exist only on the
  repo's `main`
  ([0beaa10](https://github.com/mprokopov/pi-otel-telemetry/blob/0beaa10e5bf82865260fccd3b4bc95c117d8238f/index.ts));
  the only npm release, 1.0.0 (2026-03-09), records the same 8 instruments
  with only `tool.name` on the tool metrics (checked 2026-10-19).

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
- **`pi-otel`** (NikiforovAll), opt-in with `PI_OTEL_METRICS=1`: histograms
  `gen_ai.client.operation.duration`, `gen_ai.client.token.usage`,
  `gen_ai.client.tool_calls_per_operation`, and counter
  `gen_ai.client.tool.calls` (`pi-otel@0.3.0` `dist/otel/metrics.js`,
  `dist/config.js`, checked 2026-10-19).
- **`maxmalkin/pi-OTEL`**: counters `gen_ai.client.token.usage`,
  `gen_ai.client.cost`, `pi.tool.calls`, `pi.session.compactions`,
  `pi.provider.retries`, `pi.turn.cancellations`, and histograms
  `gen_ai.client.operation.duration`, `pi.tool.duration`
  ([src/index.ts at 9c3c267](https://github.com/maxmalkin/pi-OTEL/blob/9c3c26718a005f5d658fd06f6a7fcb3e396227b4/src/index.ts),
  checked 2026-10-19).
- **`@amaster.ai/pi-telemetry`** (the connector-supported extension): no
  metrics.
- **`@the-agency/pi-observability`**: no metrics; 0.4.0 depends only on the
  OTLP trace exporter and trace SDK (checked 2026-10-19).

Full detail: [docs/metrics.md](../metrics.md#pi).

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (see
`connector/codingagentconnector/factory.go`); it does not process,
transform, or filter OTel metrics for any harness. This is also moot for Pi
specifically, since the extension the connector claims spans from exports no
metrics at all.

## Sources

Extension versions are the latest npm releases (or repository `HEAD` for
the two not published under the surveyed name), all checked 2026-10-19.

- https://github.com/earendil-works/pi/blob/v1.3.0/packages/telemetry/README.md
  — Pi's vendor-neutral telemetry contract package, no exporter; pi
  (`@earendil-works/pi-coding-agent`) 1.3.0 is the latest release
  (2026-10-15). The pre-rename `@mariozechner/pi-coding-agent` is deprecated
  in favor of it (checked 2026-10-19)
- https://www.npmjs.com/package/@amaster.ai/pi-telemetry/v/0.1.28 — the
  connector's traces source extension (published 2026-10-16); its
  `package.json` depends on no OTel logs or metrics SDK, and `dist/otel.js`
  and `dist/langfuse/exporters.js` show the resource and
  `completion_start_time` handling (checked 2026-10-19)
- Live capture, 2026-10-19: pi 1.3.0 + `@amaster.ai/pi-telemetry` 0.1.28,
  model `opencode-go/claude-haiku-5-5` (the `completion_start_time` and
  resource `telemetry.sdk.name` observations)
- https://github.com/mprokopov/pi-otel-telemetry/blob/0beaa10e5bf82865260fccd3b4bc95c117d8238f/index.ts
  — traces plus 8 metrics instruments, no logs; npm `pi-otel-telemetry@1.0.0`
  (2026-03-09, the only release) peer-depends on the deprecated
  `@mariozechner/pi-coding-agent` (checked 2026-10-19)
- https://www.npmjs.com/package/@the-agency/pi-observability/v/0.4.0 —
  traces only (repository `JoshMock/the-agency`; peer-depends on the
  deprecated `@mariozechner/pi-coding-agent`) (checked 2026-10-19)
- https://www.npmjs.com/package/pi-otel/v/0.3.0 — `pi-otel` (NikiforovAll):
  traces, opt-in logs (`PI_OTEL_LOGS=1`) and metrics (`PI_OTEL_METRICS=1`)
  (checked 2026-10-19); logs config:
  https://nikiforovall.blog/pi-otel/configuration (refreshed 2026-08-29)
- https://github.com/maxmalkin/pi-OTEL/blob/9c3c26718a005f5d658fd06f6a7fcb3e396227b4/src/index.ts
  — traces and metrics, no logs; unpublished on npm under its own name
  (`pi-otel` there is NikiforovAll's package) (checked 2026-10-19)
- https://github.com/devkade/pi-opentelemetry/tree/ac223fd08c58fe06d903c62d5c5318e1a7f9d42a/src
  — `src/trace/provider.ts` builds an `OTLPTraceExporter`;
  `src/metrics/collector.ts` / `src/metrics/provider.ts` define the metrics
  instruments; npm `@devkade/pi-opentelemetry@0.1.3` (2026-02-20) (checked
  2026-10-19)
- https://github.com/senad-d/ObservMe/blob/39c2b1f71faef63b11909f5b0385b97d16cee6e1/package.json
  — bonus find covering all three signals; not yet researched for token
  usage or repo identity (checked 2026-10-19)
- [docs/otel-signals.md](../otel-signals.md) — the full per-extension
  traces/logs/metrics survey this file draws on (refreshed 2026-08-29)
- [docs/metrics.md](../metrics.md) — Pi instrument catalog (refreshed
  2026-08-21)
- This file's Traces attribute matrix — last verified 2026-10-19 against
  live captures of `@amaster.ai/pi-telemetry` 0.1.9 (pi 0.84.2) and 0.1.28
  (pi 1.3.0)
