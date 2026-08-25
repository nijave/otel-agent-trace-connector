# Pi — raw → canonical attribute matrix

Pi (@amaster.ai/pi-telemetry) emits OTLP **traces** whose instrumentation scope
is its package name (with `telemetry.sdk.name` set to match on the resource).
Turn spans arrive as `chat-turn`, generations as `llm-generation …`, and tools
as spans named after the bare tool with the identity in attributes. The
connector claims any group carrying that scope or sdk.name and rewrites each
trace as an `invoke_agent pi` root with reparented `chat <model>` and
`execute_tool <tool>` children. Non-native spans in a claimed group (sibling
instrumentation scopes swept in by the process-wide claim) are dropped from
canonical output; the raw pipelines preserve the originals. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

### llm-generation span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `model` | `gen_ai.request.model` | mapped (also names the span) |
| `provider` | `gen_ai.provider.name` | mapped (verbatim; deliberate divergence — the value is operator-authored and often a gateway/harness label, unlike the recognized-provider values other edges emit) |
| `usage.input` | `gen_ai.usage.input_tokens` | mapped |
| `usage.output` | `gen_ai.usage.output_tokens` | mapped |
| `usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped |
| `usage.cache_read` | `gen_ai.usage.cache_read.input_tokens` | mapped |
| `usage.cache_write` | `gen_ai.usage.cache_creation.input_tokens` | mapped |
| `stopReason` | `gen_ai.response.finish_reasons` | mapped (single-element slice) |
| `responseId` | `gen_ai.response.id` | mapped |
| `usage`, `usage.cost.total` | — | dropped (deliberate: cost has no canonical counterpart) |
| `llmGenerationId` | — | dropped |
| `sessionId` | — | dropped (conversation id lives on the turn root) |
| `status` | — | dropped |
| any per-request duration field | — | not provided (no TTFT source on generation spans) |

The flat `usage.*` keys are removed per-span after mapping; the serialized
`usage` object (which additionally carries cost detail) is dropped whole.
Provider values may carry harness/route labels (for example a router or
gateway tag); they are passed through verbatim rather than normalized.

### chat-turn span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `sessionId` | `gen_ai.conversation.id` | mapped |
| `eventType` | (`coding_agent.source.event`) | dropped (the event name is pinned to `chat-turn`) |
| `durationMs` | — | dropped (whole-turn duration, not a TTFT source) |

Because `gen_ai.response.time_to_first_chunk` needs a first-chunk latency and
the wire only carries whole-turn durations, it is **not provided** for this
edge.

### tool span → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `toolName` | `gen_ai.tool.name` | mapped |
| `toolCallId` | `gen_ai.tool.call.id` | mapped |
| `status` | — | dropped |
| `sessionId` | — | dropped |

## Connector-written attributes

These are written by the connector itself, not remapped from raw keys, and
appear on every emitted span:

- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `pi` (invoke_agent root)
- `coding_agent.source` = `native`
- `coding_agent.client.name` = `pi`
- `coding_agent.client.version` (from the resource's `service.version`)
- `coding_agent.source.event` (original native span/event name)

Langfuse observation baggage (`langfuse.*`) is exporter-local metadata and is
stripped from every native span.

## Canonical keys with no Pi source

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (only whole-turn durations exist upstream) |
| `gen_ai.usage.reasoning.output_tokens` | not provided |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `gen_ai.response.model` | not provided |
| `server.address` / `server.port` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `exception.*` | not provided |
