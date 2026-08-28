# OpenCode — raw → canonical attribute matrix

OpenCode emits Vercel AI SDK spans in its native `opencode` scope
(`ai.streamText`, `ai.streamText.doStream`, `ai.toolCall` plus internal Effect
noise). The connector claims groups containing that scope, keeps only the three
claimed span names, renames them, remaps their vendor attributes onto the
canonical vocabulary, and drops everything else — including internal Effect
spans and non-`opencode` scopes. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

Span-name renames:

| Raw span name | Canonical span name |
|---|---|
| `ai.streamText` | `invoke_agent opencode` (re-rooted) |
| `ai.streamText.doStream` | `chat <model>` |
| `ai.toolCall` | `execute_tool <tool>` |
| everything else (Effect internals, plugins) | dropped |

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

Usage is per step and never zero-filled: an in-flight or failed step carries
no `ai.usage.*` on the wire, so its canonical span emits with no
`gen_ai.usage.*` keys at all.

### ai.streamText → invoke_agent

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

### ai.streamText.doStream → chat

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

### ai.toolCall → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `ai.toolCall.name` | `gen_ai.tool.name` | mapped (a call without it emits as bare `execute_tool` with no key) |
| `ai.model.provider` | `gen_ai.provider.name` | mapped (only when non-empty; the pinned capture's tool spans carry no provider) |
| `ai.toolCall.id` | `gen_ai.tool.call.id` | not mapped today (dropped) |
| `ai.toolCall.args` | — | dropped (tool input content) |
| `ai.toolCall.result` | — | dropped (tool output content) |
| `ai.operationId`, `ai.telemetry.functionId` | — | dropped |
| `operation.name`, `resource.name`, `session.id` | — | dropped |

## Connector-written attributes

The connector writes these itself rather than remapping them from raw keys:

- `coding_agent.source` = `native`
- `coding_agent.client.name` = `opencode`
- `coding_agent.client.version` = resource `service.version` (when present)
- `coding_agent.source.event` = original `ai.*` span name on all claimed spans
- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `opencode` (invoke_agent root only)

## Canonical keys with no OpenCode source

| Canonical key | Status |
|---|---|
| `gen_ai.usage.cache_write.input_tokens` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `gen_ai.request.stream` | not provided (all calls stream) |
| `gen_ai.conversation.id` on chat/execute_tool spans | not mapped (parent carries it) |
| `gen_ai.tool.type` / `gen_ai.tool.status` | not provided |
| `server.address` / `server.port` | not provided |
