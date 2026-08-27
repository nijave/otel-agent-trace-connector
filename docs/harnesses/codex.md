# Codex — raw → canonical attribute matrix

Codex emits structured OTLP **logs** (`codex.*` events with `conversation.id`)
rather than spans. The connector is a logs-to-traces edge: it deduplicates
redelivered records, correlates each turn's events into a deterministic trace,
and emits one `invoke_agent codex` root with `chat <model>` and
`execute_tool <tool>` children plus the remaining raw events attached to the
root as span events (names only). See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

Event-to-span mapping:

| Raw log event | Canonical output |
|---|---|
| `codex.sse_event` (`event.kind=response.completed`, usage/ttft-bearing) | `chat <model>` span |
| duplicate timing-only `response.completed` (no `ttft_ms`, no token counts) | dropped |
| `codex.user_prompt` | turn boundary / root source event (content stripped) |
| `codex.tool_result` | `execute_tool <tool>` span |
| `codex.api_request`, `codex.startup_phase`, … | root span events (name + timestamp only) |
| `codex.tool_decision` | dropped entirely |
| everything else | root span events (name + timestamp only) |

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

Usage is provider-dependent: the connector copies only the counts the
provider returned. A `response.completed` from a provider that reports no
usage (e.g. a chat-completions stream without `stream_options.include_usage`)
still builds its chat span, with no `gen_ai.usage.*` keys at all.

Wire drift (audited 2026-08-25 against upstream HEAD): the pinned e2e capture
now comes from Codex 0.150.1. That version sends `cache_write_token_count` on
usage-bearing `response.completed` records (mapped above) and extra detail
fields on `codex.tool_result` (`tool_namespace`, `tool_result_seq`,
`output_truncated`, `agent_name`); `codex.tool_decision` gains
`tool_namespace` too. The builder copies only the keys in this matrix, so
those detail fields stay out of canonical output.

### codex.sse_event (response.completed) → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `input_token_count` | `gen_ai.usage.input_tokens` | mapped (absent when the provider reports no usage) |
| `output_token_count` | `gen_ai.usage.output_tokens` | mapped (absent when the provider reports no usage) |
| `cached_token_count` | `gen_ai.usage.cache_read.input_tokens` | mapped (absent when the provider's usage carries no cached-token field) |
| `cache_write_token_count` | `gen_ai.usage.cache_creation.input_tokens` | mapped (absent when the provider's usage carries no cache-write field; the pinned 0.150.1 capture reports 0) |
| `tool_token_count` | `gen_ai.usage.total_tokens` | mapped (absent when the provider's usage carries no total; the connector never computes one) |
| `reasoning_token_count` | `gen_ai.usage.reasoning.output_tokens` | mapped (absent when the provider's usage carries no reasoning field; replaces the former vendor `coding_agent.usage.reasoning_tokens`) |
| `ttft_ms` | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; absent when a usage-bearing completion carries no `ttft_ms` — either `ttft_ms` or a token count keeps a completion, and a record with neither drops as the timing-only duplicate) |
| `model` | `gen_ai.request.model` | mapped (also names the span) |
| `model_reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (absent when the build sends no effort value — reasoning effort "none" sends nothing — and absent from the pinned 0.150.1 capture) |
| `service_tier` | `coding_agent.request.service_tier` | mapped (absent when the build sends no tier or the provider returns none; absent from the pinned 0.150.1 capture) |
| `duration_ms` | — | dropped (used for span bounds only) |
| `event.kind`, `event.timestamp` | — | dropped (span name/bounds carry them) |
| `slug`, `originator`, `terminal.type`, `attempt`, `endpoint`, auth/http detail | — | dropped |

### codex.tool_result → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `tool_name` | `gen_ai.tool.name` | mapped (also names the span; a record without `tool_name` gets the literal `unknown`) |
| `success` = false | span Status = Error, message `tool execution failed` | mapped to status (the connector drops the boolean attribute itself) |
| `call_id` | — | dropped (the connector does not emit `gen_ai.tool.call.id`) |
| `duration_ms` | — | dropped (used for span bounds only) |
| `arguments`, `output` | — | dropped (tool content, stripped at parse time) |
| `mcp_server`, `mcp_server_origin` | — | dropped |

### invoke_agent codex root

| Raw key | Canonical key | Status |
|---|---|---|
| `conversation.id` | `gen_ai.conversation.id` | mapped |
| `model` | `gen_ai.request.model` | mapped (last observed value in the turn) |
| `app.version` | `coding_agent.client.version` | mapped |
| `provider_name` | — | dropped (operator-authored display label from config.toml, not a known provider identifier; `gen_ai.provider.name` stays `openai`, the wire protocol). Only the session's first turn logs it anyway |
| `coding_agent.turn.finish_reason` | — | dropped (was **connector-derived** — `completed`/`superseded`/`timeout`/`evicted`/`shutdown` finalization reasons, not a model stop reason; still exposed as the `otelcol_coding_agent_turns_emitted` metric's `finish_reason` label. Timeouts surface as root span Status Error) |
| `coding_agent.turn.complete` | — | dropped (derivable from the metric label above) |
| `coding_agent.turn.prompt_observed` | — | dropped |
| `coding_agent.turn.events_truncated` | — | dropped (still exposed via `otelcol_coding_agent_turns_truncated`) |
| turn-total usage (summed token counts on the root) | — | dropped (usage lives on chat spans only; sum them for turn totals) |
| root-event `error.message` copy | — | dropped (root events keep their names only) |
| root-event `event.kind` copy | — | dropped |

### codex.tool_decision

Dropped entirely, including the decision→result pairing: its only canonical
outputs were `coding_agent.tool.decision` / `coding_agent.tool.decision_source`
(both vendor keys outside the vocabulary), and once those went the join had no
remaining purpose.

## Connector-written attributes

The connector writes these itself rather than remapping them from raw keys;
they appear on every emitted span except where noted:

- `coding_agent.source` = `normalized`
- `coding_agent.client.name` = `codex`
- `coding_agent.source.event` = originating `codex.*` event name
- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.provider.name` = `openai` (the wire protocol; invoke_agent root and chat spans only — execute_tool spans never carry it)
- `gen_ai.agent.name` = `codex` (invoke_agent root only)

## Canonical keys with no Codex source

| Canonical key | Status |
|---|---|
| `gen_ai.response.finish_reasons` | not provided (Codex logs no model stop reason; see the dropped connector-derived `finish_reason` above) |
| `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `gen_ai.tool.call.id` / `gen_ai.tool.type` / `gen_ai.tool.status` | not provided |
| `server.address` / `server.port` | not provided (proxied setups are indistinguishable) |
| `exception.*` | not provided |
