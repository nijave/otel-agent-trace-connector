# Pi — raw → canonical attribute matrix

Pi (@amaster.ai/pi-telemetry) emits OTLP **traces** whose instrumentation scope
is its package name (with `telemetry.sdk.name` set to match on the resource).
Turn spans arrive as `chat-turn`, generations as `llm-generation …`, and tools
as spans named after the bare tool with the identity in attributes. The
connector claims any group carrying that scope or sdk.name and rewrites each
trace as an `invoke_agent pi` root with reparented `chat <model>` and
`execute_tool <tool>` children. Non-native spans in a claimed group (sibling
instrumentation scopes swept in by the process-wide claim) drop from
canonical output; the raw pipelines preserve the originals. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

### llm-generation span → chat

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

### chat-turn span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `sessionId` | `gen_ai.conversation.id` | mapped (`chat-turn` spans only; Pi exports children in batches without their turn, and such a batch emits `chat`/`execute_tool` spans with no conversation id anywhere — the `sessionId` they carry strips) |
| `eventType` | (`coding_agent.source.event`) | dropped (the connector pins the event name to `chat-turn`) |
| `durationMs` | — | dropped (whole-turn duration, not a TTFT source) |

Because `gen_ai.response.time_to_first_chunk` needs a first-chunk latency and
the wire only carries whole-turn durations, the key stays **not provided**
for this edge.

### tool span → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `toolName` | `gen_ai.tool.name` | mapped (also the discriminator: only spans carrying `toolName` become `execute_tool`) |
| `toolCallId` | `gen_ai.tool.call.id` | mapped (when non-empty; `toolName` alone still produces the span) |
| `status` | — | dropped |
| `sessionId` | — | dropped (see the `chat-turn` row for the split-batch consequence) |

## Connector-written attributes

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
