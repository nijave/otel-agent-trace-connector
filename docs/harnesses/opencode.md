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
| Traces | native since releases ≥1.18.21 (2026-08-21); Vercel AI SDK spans in the native `opencode` scope, enabled via `experimental.openTelemetry: true` | traces edge (below) |
| Logs | native — Effect spans/logs, exported whenever `OTEL_EXPORTER_OTLP_ENDPOINT` points at a collector, independent of the trace path | not consumed |
| Metrics | none native (`experimental.openTelemetry` is traces-focused and reported broken for `opencode run`); available only via plugins | not applicable |

Last verified: Traces 2026-08-27, Logs 2026-08-29, Metrics 2026-08-21 (see
Sources).

## Traces

### Native support

Two independent native paths, both under `experimental.openTelemetry: true`:
Vercel AI SDK spans (`ai.streamText`, `ai.toolCall`), with every span getting
`session.id` (`ses_…`) via a tracer proxy, and Effect spans
(`Tool.execute`). This section covers the Vercel AI SDK path only, the one
the connector's OpenCode edge claims.

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
| `ai.telemetry.functionId` | — | dropped |
| `ai.telemetry.metadata.sessionId` | — | dropped (the edge reads `session.id` instead) |
| `ai.telemetry.metadata.userId` | — | dropped |
| `ai.request.headers.*` | — | dropped |
| `ai.prompt`, `ai.prompt.messages`, `ai.prompt.tools`, `ai.prompt.toolChoice` | — | dropped (prompt content) |
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
| `gen_ai.request.model` | `gen_ai.request.model` | kept (already canonical; a doStream without it emits as bare `chat` with no key) |
| `ai.model.provider` | `gen_ai.provider.name` | mapped (only when non-empty; an empty wire provider stays absent) |
| `ai.usage.inputTokens` | `gen_ai.usage.input_tokens` | mapped (absent for in-flight or failed steps) |
| `ai.usage.outputTokens` | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `ai.usage.cachedInputTokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition) |
| `ai.usage.totalTokens` | `gen_ai.usage.total_tokens` | mapped (same condition) |
| `ai.usage.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (falls back to `ai.usage.outputTokenDetails.reasoningTokens`; absent for non-reasoning steps) |
| `ai.usage.outputTokenDetails.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (fallback source only) |
| `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | same key | kept (already canonical passthrough) |
| `ai.response.msToFirstChunk` | `gen_ai.response.time_to_first_chunk` | mapped (fractional ms → seconds, double; absent when the request errors before the first chunk) |
| `session.id` | `gen_ai.conversation.id` | not mapped on this span (the parent `invoke_agent` carries it) |
| `gen_ai.system` | — | dropped (`gen_ai.provider.name` comes from `ai.model.provider` instead) |
| `gen_ai.request.max_tokens` | — | dropped today; candidate future mapping (`ai.settings.maxOutputTokens` duplicates it) |
| `gen_ai.response.finish_reasons` | — | dropped (deliberate: wire value reflects the SDK's finish event, not a normalized stop-reason set) |
| `gen_ai.response.id` | — | dropped (deliberate) |
| `gen_ai.response.model` | — | dropped |
| `ai.response.finishReason` | — | dropped |
| `ai.response.msToFinish` | — | dropped |
| `ai.response.avgOutputTokensPerSecond` | — | dropped |
| `ai.response.id`, `ai.response.model`, `ai.response.timestamp` | — | dropped |
| `ai.response.toolCalls`, `ai.response.providerMetadata` | — | dropped |
| `ai.response.text`, `ai.response.reasoning` | — | dropped (completion content) |
| `ai.request.headers.*` | — | dropped |
| `ai.prompt.*` | — | dropped (prompt content) |
| `ai.telemetry.functionId` | — | dropped |
| `ai.telemetry.metadata.userId` | — | dropped |
| `ai.telemetry.metadata.sessionId` | — | dropped |
| `ai.model.id` | — | dropped |
| `ai.settings.maxOutputTokens`, `ai.settings.maxRetries` | — | dropped |
| `ai.usage.inputTokenDetails.noCacheTokens` | — | dropped |
| `ai.usage.inputTokenDetails.cacheReadTokens` | — | dropped (redundant duplicate of `ai.usage.cachedInputTokens`) |
| `ai.usage.outputTokenDetails.textTokens` | — | dropped |

The edge drops the span events (`ai.stream.firstChunk`, `ai.stream.finish`);
first-chunk latency survives via the `msToFirstChunk` mapping above.

##### ai.toolCall → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `ai.toolCall.name` | `gen_ai.tool.name` | mapped (a call without it emits as bare `execute_tool` with no key) |
| `ai.model.provider` | `gen_ai.provider.name` | mapped (only when non-empty; the pinned capture's tool spans carry no provider) |
| `ai.toolCall.id` | `gen_ai.tool.call.id` | not mapped today (dropped) |
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

No native OTel metrics — `experimental.openTelemetry` is traces-focused and
reported broken for `opencode run`. Two independent plugins fill the gap
(full attribute tables in [docs/metrics.md](../metrics.md)):

- **`@devtheops/opencode-plugin-otel`** mirrors Claude Code's metric
  catalog: `opencode.session.count`, `opencode.token.usage`,
  `opencode.cost.usage`, `opencode.lines_of_code.count`/`.total`,
  `opencode.commit.count`, `opencode.tool.duration`, `opencode.cache.count`,
  `opencode.session.duration`, `opencode.message.count`,
  `opencode.session.token.total`, `opencode.session.cost.total`,
  `opencode.model.usage`, `opencode.retry.count`, `opencode.subtask.count`.
- **`@gcornut/opencode-otel`** offers 8 counters with a
  `telemetryProfile: "claude-code"` rename.

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines
(`connector/codingagentconnector/factory.go`); it does not process,
transform, or filter OTel metrics for any harness, and neither plugin above
is native to OpenCode itself.

## Sources

- OpenCode: https://github.com/sst/opencode — traces signal and the
  attribute matrix above, last verified 2026-08-27
- OpenCode plugin `@devtheops/opencode-plugin-otel`:
  https://github.com/DEVtheOPS/opencode-plugin-otel — metrics catalog
- [docs/otel-signals.md](../otel-signals.md) — traces/logs/metrics signal
  summary, refreshed 2026-08-29
- [docs/metrics.md](../metrics.md) — full metrics instrument catalog,
  refreshed 2026-08-21
