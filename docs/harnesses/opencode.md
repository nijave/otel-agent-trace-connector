# OpenCode — OpenTelemetry signal reference

OpenCode exports two independent native OTel paths: Vercel AI SDK **traces**
(`ai.streamText`, `ai.streamText.doStream`, `ai.toolCall` plus internal
Effect noise) in the native `opencode` scope, and separate Effect **logs**.
The connector claims groups containing the traces scope, keeps only the
three claimed span names, renames them, remaps their vendor attributes onto
the canonical vocabulary, and drops everything else — including internal
Effect spans, Effect logs, and non-`opencode` scopes. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | native since v1.4.5 (2026-04-15, "Export AI SDK telemetry spans to OTLP trace backends"); Vercel AI SDK spans in the native `opencode` scope, exported when `OTEL_EXPORTER_OTLP_ENDPOINT` is set and `experimental.openTelemetry: true` (the key is still under `experimental` in 1.19.3); absent when `OPENCODE_EXPERIMENTAL_NATIVE_LLM=true` routes a supported provider around the AI SDK | traces edge (below) |
| Logs | native — Effect spans/logs, exported whenever `OTEL_EXPORTER_OTLP_ENDPOINT` points at a collector, independent of the trace path | not consumed |
| Metrics | none native (OpenCode registers only OTLP trace and log exporters); available only via plugins | not applicable |

Last verified: Traces, Logs, and Metrics 2026-10-19 (OpenCode 1.19.3; see
Sources).

## Traces

### Native support

Two native span paths share the `opencode` scope. Effect spans
(`Tool.execute` and other internals) export whenever
`OTEL_EXPORTER_OTLP_ENDPOINT` is set. Vercel AI SDK spans (`ai.streamText`,
`ai.streamText.doStream`, `ai.toolCall`) also need
`experimental.openTelemetry: true`; every session LLM span gets `session.id`
(`ses_…`) via a tracer proxy. With `OPENCODE_EXPERIMENTAL_NATIVE_LLM=true`, a
supported provider (OpenAI, OpenCode-managed OpenAI-compatible, Anthropic API
key) takes OpenCode's native LLM runtime instead of the AI SDK and emits no
`ai.*` spans, so the connector sees nothing for that turn. This section covers
the Vercel AI SDK path only, the one the connector's OpenCode edge claims.

Span-name renames:

| Raw span name | Canonical span name |
|---|---|
| `ai.streamText` | `invoke_agent opencode` (re-rooted) |
| `ai.streamText.doStream` | `chat <model>` |
| `ai.toolCall` | `execute_tool <tool>` |
| everything else (Effect internals, plugins) | dropped |

### Connector mapping

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

Usage is per step and never zero-filled: an in-flight or failed step carries
no `ai.usage.*` on the wire, so its canonical span emits with no
`gen_ai.usage.*` keys at all.

##### ai.streamText → invoke_agent

| Raw key | Canonical key | Status |
|---|---|---|
| `session.id` | `gen_ai.conversation.id` | mapped (resource `session.id` as fallback) |
| `ai.usage.inputTokens` | `gen_ai.usage.input_tokens` | mapped (absent for in-flight or failed steps) |
| `ai.usage.outputTokens` | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `ai.usage.cachedInputTokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition) |
| `ai.usage.totalTokens` | `gen_ai.usage.total_tokens` | mapped (same condition) |
| `ai.model.provider` | `gen_ai.provider.name` | mapped (only when non-empty; an empty wire provider stays absent) |
| `ai.model.id` | — | dropped (`gen_ai.request.model` carries the model) |
| `ai.settings.maxOutputTokens` | — | dropped |
| `ai.settings.maxRetries` | — | dropped |
| `ai.settings.temperature` | — | dropped (only sent when the caller pins a temperature; the pinned 1.19.3 capture carries 0.5 on the title-generator call and omits it on the main turn) |
| `ai.operationId`, `ai.telemetry.functionId` | — | dropped |
| `ai.telemetry.metadata.sessionId` | — | dropped (the edge reads `session.id` instead) |
| `ai.telemetry.metadata.userId` | `coding_agent.user.id` | mapped (only when `capture_identity` is on; the wire value is the literal `unknown` unless the config sets `username`) |
| `ai.request.headers.*` — including `x-opencode-session`, `x-opencode-session-id`, `x-opencode-client`, `x-opencode-project`, `x-opencode-request`, `User-Agent` | — | dropped (per-request routing/transport headers; the ids they repeat already map from `session.id` and `ai.telemetry.metadata.userId`) |
| `ai.prompt` | — | dropped (prompt content) |
| `ai.response.text`, `ai.response.reasoning` | — | dropped (completion content) |
| `ai.response.toolCalls`, `ai.response.providerMetadata` | — | dropped |
| `ai.response.finishReason` | — | dropped |
| `ai.response.id`, `ai.response.model`, `ai.response.timestamp` | — | dropped |
| `ai.usage.inputTokenDetails.noCacheTokens` | — | dropped |
| `ai.usage.inputTokenDetails.cacheReadTokens` | — | dropped (redundant duplicate of `ai.usage.cachedInputTokens`) |
| `ai.usage.outputTokenDetails.textTokens` | — | dropped |
| `ai.usage.reasoningTokens` / `ai.usage.outputTokenDetails.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (fallback order as listed; absent for non-reasoning steps; on the wire from opencode 1.18.21) |
| `operation.name`, `resource.name` | — | dropped (Effect bookkeeping) |

##### ai.streamText.doStream → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `gen_ai.request.model` | `gen_ai.request.model` | mapped (already canonical; absent when the wire omits it, and the span then emits as bare `chat`) |
| `ai.model.provider` | `gen_ai.provider.name` | mapped (only when non-empty; an empty wire provider stays absent) |
| `ai.usage.inputTokens` | `gen_ai.usage.input_tokens` | mapped (absent for in-flight or failed steps) |
| `ai.usage.outputTokens` | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `ai.usage.cachedInputTokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition) |
| `ai.usage.totalTokens` | `gen_ai.usage.total_tokens` | mapped (same condition) |
| `ai.usage.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (falls back to `ai.usage.outputTokenDetails.reasoningTokens`; absent for non-reasoning steps) |
| `ai.usage.outputTokenDetails.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (fallback source only) |
| `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | same key | mapped (already canonical passthrough; absent for in-flight or failed steps) |
| `ai.response.msToFirstChunk` | `gen_ai.response.time_to_first_chunk` | mapped (fractional ms → seconds, double; absent when the request errors before the first chunk) |
| `session.id` | — | dropped on this span (the parent `invoke_agent` maps it to `gen_ai.conversation.id`) |
| `gen_ai.system` | — | dropped (`gen_ai.provider.name` comes from `ai.model.provider` instead) |
| `gen_ai.request.max_tokens` | `gen_ai.request.max_tokens` | mapped (already canonical; absent when the caller pins no output-token cap) |
| `gen_ai.request.temperature` | — | dropped (only sent when the caller pins a temperature; the pinned 1.19.3 capture carries 0.5 on the title-generator call and omits it on the main turn) |
| `gen_ai.response.finish_reasons` | `gen_ai.response.finish_reasons` | mapped (verbatim wire array, one element per step; values are the SDK's finish events — `stop`, `tool-calls`, `error`, … — not a normalized stop-reason set; absent when the request fails or the stream is cancelled before the SDK records its finish event) |
| `gen_ai.response.id` | `gen_ai.response.id` | mapped (verbatim; absent when the provider's stream reports no response id) |
| `gen_ai.response.model` | `gen_ai.response.model` | mapped (verbatim; absent when the provider's stream reports no response model) |
| `ai.response.finishReason` | — | dropped (duplicates `gen_ai.response.finish_reasons`) |
| `ai.response.msToFinish` | — | dropped |
| `ai.response.avgOutputTokensPerSecond` | — | dropped |
| `ai.response.id`, `ai.response.model` | — | dropped (duplicate the `gen_ai.response.*` keys) |
| `ai.response.timestamp` | — | dropped |
| `ai.response.toolCalls`, `ai.response.providerMetadata` | — | dropped |
| `ai.response.text`, `ai.response.reasoning` | — | dropped (completion content) |
| `ai.request.headers.*` — including `x-opencode-session`, `x-opencode-session-id`, `x-opencode-client`, `x-opencode-project`, `x-opencode-request`, `User-Agent` | — | dropped (per-request routing/transport headers) |
| `ai.prompt.*` | — | dropped (prompt content) |
| `ai.operationId`, `ai.telemetry.functionId` | — | dropped |
| `ai.telemetry.metadata.userId` | — | dropped |
| `ai.telemetry.metadata.sessionId` | — | dropped |
| `ai.model.id` | — | dropped |
| `ai.settings.maxOutputTokens` | — | dropped (duplicates `gen_ai.request.max_tokens`) |
| `ai.settings.maxRetries` | — | dropped |
| `ai.settings.temperature` | — | dropped (only sent when the caller pins a temperature; the pinned 1.19.3 capture carries 0.5 on the title-generator call and omits it on the main turn) |
| `ai.usage.inputTokenDetails.noCacheTokens` | — | dropped |
| `ai.usage.inputTokenDetails.cacheReadTokens` | — | dropped (redundant duplicate of `ai.usage.cachedInputTokens`) |
| `ai.usage.outputTokenDetails.textTokens` | — | dropped |

The edge drops the span events (`ai.stream.firstChunk`, `ai.stream.finish`);
first-chunk latency survives via the `msToFirstChunk` mapping above.

The response identity, finish reasons, and requested output cap above are
copied from the wire's own canonical keys; the AI SDK's legacy `ai.response.*`
/ `ai.settings.maxOutputTokens` spellings carry the same values in the same
`setAttributes` call ([Sources](#sources)) and stay off canonical output.

Capture shape: OpenCode also makes an unrequested model call to generate the
session title, so a pinned capture holds two model conversations — the title
generator, then the main turn — and replays as two `invoke_agent opencode`
roots (the 1.19.3 fixture has two chat spans and one `execute_tool bash`).

##### ai.toolCall → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `ai.toolCall.name` | `gen_ai.tool.name` | mapped (a call without it emits as bare `execute_tool` with no key) |
| `ai.model.provider` | `gen_ai.provider.name` | mapped (only when non-empty; the pinned capture's tool spans carry no provider) |
| `ai.toolCall.id` | `gen_ai.tool.call.id` | mapped (when non-empty; AI SDK 6.0.168 sets it on every tool-call span) |
| `ai.toolCall.args` | — | dropped (tool input content) |
| `ai.toolCall.result` | — | dropped (tool output content) |
| `ai.operationId`, `ai.telemetry.functionId` | — | dropped |
| `operation.name`, `resource.name`, `session.id` | — | dropped |

#### Connector-written attributes

The connector writes these itself rather than remapping them from raw keys:

- `coding_agent.source` = `native`
- `coding_agent.client.name` = `opencode`
- `coding_agent.client.version` = resource `service.version` (when present)
- `coding_agent.source.event` = original `ai.*` span name on all claimed spans
- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `opencode` (invoke_agent root only)

#### Canonical keys with no OpenCode source

| Canonical key | Status |
|---|---|
| `gen_ai.usage.cache_write.input_tokens` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `gen_ai.request.stream` | not provided (all calls stream) |
| `gen_ai.conversation.id` on chat/execute_tool spans | not mapped (parent carries it) |
| `gen_ai.tool.type` / `gen_ai.tool.status` | not provided |
| `server.address` / `server.port` | not provided |
| `coding_agent.usage.cost` | not provided (the AI SDK spans carry no cost) |

## Logs

### Native support

OpenCode's second native path — Effect spans (`Tool.execute` with
`tool.name`/`message.id`/`tool.call_id`) and Effect **logs** — exports
whenever `OTEL_EXPORTER_OTLP_ENDPOINT` points at a collector, independent of
the `experimental.openTelemetry` trace flag above. See
[docs/harnesses.md](../harnesses.md) and
[docs/otel-signals.md](../otel-signals.md) for the upstream research.

### Connector mapping

Not consumed. The connector's OpenCode edge claims only the `opencode`
scope's Vercel AI SDK trace spans (above); it does not claim Effect logs, so
no canonical output derives from this signal today.

## Metrics

### Native support

No native OTel metrics: OpenCode's OTLP setup builds only a trace exporter
and a log exporter. Two independent plugins fill the gap (full attribute
tables in [docs/metrics.md](../metrics.md)):

- **`@devtheops/opencode-plugin-otel`** mirrors Claude Code's metric
  catalog: `opencode.session.count`, `opencode.token.usage`,
  `opencode.cost.usage`, `opencode.commit.count`, `opencode.tool.duration`, `opencode.cache.count`,
  `opencode.session.duration`, `opencode.message.count`,
  `opencode.session.token.total`, `opencode.session.cost.total`,
  `opencode.model.usage`, `opencode.retry.count`, `opencode.subtask.count`.
  v2 no longer emits `lines_of_code.count`/`.total`.
- **`@gcornut/opencode-otel`** offers 8 counters with a
  `telemetryProfile: "claude-code"` rename.

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines
(`connector/codingagentconnector/factory.go`); it does not process,
transform, or filter OTel metrics for any harness, and neither plugin above
is native to OpenCode itself.

## Sources

- OpenCode: https://github.com/anomalyco/opencode (formerly `sst/opencode`)
  — traces signal and the attribute matrix above, last verified 2026-10-19
  against OpenCode 1.19.3 and the live capture of 2026-10-19 (OpenCode 1.19.3,
  `opencode-go` provider, `glm-5.3-flash`)
- OpenCode v1.19.3 source, checked 2026-10-19:
  - [`packages/core/src/observability/otlp.ts`](https://github.com/anomalyco/opencode/blob/v1.19.3/packages/core/src/observability/otlp.ts#L50-L76)
    — log and trace exporters gated only on `OTEL_EXPORTER_OTLP_ENDPOINT`; no
    metrics exporter
  - [`packages/opencode/src/session/llm.ts`](https://github.com/anomalyco/opencode/blob/v1.19.3/packages/opencode/src/session/llm.ts#L208-L262)
    — `experimental.openTelemetry` tracer, `session.id` proxy,
    `OPENCODE_EXPERIMENTAL_NATIVE_LLM` runtime switch; `userId` defaults to
    `unknown` ([L344-L351](https://github.com/anomalyco/opencode/blob/v1.19.3/packages/opencode/src/session/llm.ts#L344-L351))
  - [`packages/core/src/v1/config/config.ts`](https://github.com/anomalyco/opencode/blob/v1.19.3/packages/core/src/v1/config/config.ts#L173-L175)
    — `experimental.openTelemetry` config key
  - [`packages/opencode/src/session/llm/AGENTS.md`](https://github.com/anomalyco/opencode/blob/v1.19.3/packages/opencode/src/session/llm/AGENTS.md#L86-L89)
    — providers the native LLM runtime supports
- Vercel AI SDK `ai` v6.0.168 (the version OpenCode 1.19.3 pins,
  [`package.json`](https://github.com/anomalyco/opencode/blob/v1.19.3/package.json#L67)),
  checked 2026-10-19:
  - [`packages/ai/src/generate-text/stream-text.ts`](https://github.com/vercel/ai/blob/ai@6.0.168/packages/ai/src/generate-text/stream-text.ts#L1642-L1652)
    — the `ai.streamText.doStream` request attributes, including
    `gen_ai.request.max_tokens` from `callSettings.maxOutputTokens`
  - [same file, response attributes](https://github.com/vercel/ai/blob/ai@6.0.168/packages/ai/src/generate-text/stream-text.ts#L1943-L1980)
    — `gen_ai.response.finish_reasons` as a one-element array,
    `gen_ai.response.id` / `gen_ai.response.model` from the provider's stream
    response, alongside the legacy `ai.response.finishReason` / `.id` /
    `.model` twins
  - [`packages/ai/src/generate-text/execute-tool-call.ts`](https://github.com/vercel/ai/blob/ai@6.0.168/packages/ai/src/generate-text/execute-tool-call.ts#L88-L89)
    — `ai.toolCall.id` on every tool-call span
  - [`packages/ai/src/telemetry/select-telemetry-attributes.ts`](https://github.com/vercel/ai/blob/ai@6.0.168/packages/ai/src/telemetry/select-telemetry-attributes.ts#L30)
    — undefined attribute values never reach the span, so an unset
    `maxOutputTokens` leaves `gen_ai.request.max_tokens` absent
- OpenCode v1.4.5 release notes (2026-04-15):
  https://github.com/anomalyco/opencode/releases/tag/v1.4.5 and
  https://github.com/anomalyco/opencode/pull/22526 — first release exporting
  AI SDK spans, checked 2026-10-19
- OpenCode plugin `@devtheops/opencode-plugin-otel` v2.0.1:
  https://github.com/DEVtheOPS/opencode-plugin-otel/blob/v2.0.1/README.md
  — metrics catalog, checked 2026-10-19
- OpenCode plugin `@gcornut/opencode-otel`:
  https://github.com/gcornut/opencode-otel/blob/68f2caf9f7f01530f6bafa07a2cdd6b17aee5316/README.md
  — 8 counters and `telemetryProfile`, checked 2026-10-19
- [docs/otel-signals.md](../otel-signals.md) — traces/logs/metrics signal
  summary, refreshed 2026-08-29
- [docs/metrics.md](../metrics.md) — full metrics instrument catalog,
  refreshed 2026-08-21
