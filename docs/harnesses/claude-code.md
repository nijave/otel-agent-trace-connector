# Claude Code — raw → canonical attribute matrix

Claude Code emits native `claude_code.*` spans (scope
`com.anthropic.claude_code.tracing`). The connector claims groups containing
those spans, renames the three top-level span types, remaps their vendor
attributes onto the canonical vocabulary, and strips every attribute outside
that vocabulary from every span in the group. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

Span-name renames:

| Raw span name | Canonical span name |
|---|---|
| `claude_code.interaction` | `invoke_agent claude_code` |
| `claude_code.llm_request` | `chat <model>` |
| `claude_code.tool` | `execute_tool <tool>` |
| other `claude_code.*` sub-spans | unchanged |

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

### claude_code.interaction

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `session.id` | interaction | `gen_ai.conversation.id` | mapped |
| `user_prompt` | interaction | — | dropped |
| `user_prompt_length` | interaction | — | dropped |
| `interaction.sequence` | interaction | — | dropped |
| `interaction.duration_ms` | interaction | — | dropped |
| `user.id` | interaction | — | dropped |
| `terminal.type` | interaction | — | dropped |
| `span.type` | interaction | — | dropped |

### claude_code.llm_request

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `input_tokens` | llm_request | `gen_ai.usage.input_tokens` | mapped |
| `output_tokens` | llm_request | `gen_ai.usage.output_tokens` | mapped |
| `cache_read_tokens` | llm_request | `gen_ai.usage.cache_read.input_tokens` | mapped |
| `cache_creation_tokens` | llm_request | `gen_ai.usage.cache_creation.input_tokens` | mapped |
| `ttft_ms` | llm_request | `gen_ai.response.time_to_first_chunk` | mapped (ms) |
| `stop_reason` | llm_request | `gen_ai.response.finish_reasons` | mapped (appended if absent) |
| `model` | llm_request | `gen_ai.request.model` | mapped (fallback when the canonical key is absent) |
| `gen_ai.response.finish_reasons` | llm_request | `gen_ai.response.finish_reasons` | kept (already canonical) |
| `duration_ms` | llm_request | — | dropped |
| `speed` | llm_request | — | dropped |
| `llm_request.context` | llm_request | — | dropped |
| `attempt` | llm_request | — | dropped |
| `success` | llm_request | — | dropped |
| `gen_ai.system` | llm_request | — | dropped (`gen_ai.provider.name` is connector-derived, not remapped) |
| `session.id`, `user.id`, `terminal.type`, `span.type` | llm_request | — | dropped |

### claude_code.tool

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `tool_name` | tool | `gen_ai.tool.name` | mapped |
| `gen_ai.tool.call.id` | tool / tool.execution | `gen_ai.tool.call.id` | kept (already canonical) |
| `tool_use_id` | tool / tool.execution | — | dropped |
| `duration_ms` | tool / tool.execution | — | dropped |
| `success` | tool.execution | — | dropped (failure still maps to span Status Error upstream of this connector's vocabulary where applicable) |
| `decision` | tool.blocked_on_user | — | dropped |
| `source` | tool.blocked_on_user | — | dropped |
| `session.id`, `user.id`, `terminal.type`, `span.type` | all tool spans | — | dropped |

Unrenamed sub-spans (`claude_code.tool.execution`,
`claude_code.tool.blocked_on_user`, hooks) receive the required provenance keys
(`gen_ai.operation.name`, `coding_agent.source`, `coding_agent.client.name`)
but are otherwise stripped like every other span.

### Span events

| Raw event | Canonical fate | Status |
|---|---|---|
| `gen_ai.request.attempt` (attr `attempt`) | event survives, non-canonical attributes stripped | dropped |

## Canonical keys with no Claude Code source

| Canonical key | Status |
|---|---|
| `gen_ai.usage.total_tokens` | not provided (no raw total-token source) |
| `gen_ai.usage.reasoning.output_tokens` | not provided (no reasoning-token source) |
| `gen_ai.response.id` | not provided |
| `gen_ai.response.model` | not provided |
| `gen_ai.request.max_tokens` | not provided |
| `gen_ai.request.stream` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `server.address` / `server.port` | not provided |

## Connector-written attributes

These are written by the connector itself, not remapped from raw keys:

- `coding_agent.source` = `native`
- `coding_agent.client.name` = `claude_code`
- `coding_agent.client.version` = resource `service.version`
- `coding_agent.source.event` = original `claude_code.*` span name on the
  three renamed types
- `gen_ai.provider.name` = `anthropic`
- `gen_ai.operation.name`, `gen_ai.agent.name` (interaction root)
