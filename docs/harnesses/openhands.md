# OpenHands — raw → canonical attribute matrix

OpenHands emits OTLP **traces** on scope `lmnr.tracer` (Laminar's SDK), with
LiteLLM-style attributes on LLM spans and conversation-level state in
`lmnr.association.properties.*`. The connector claims only groups carrying an
explicit OpenHands marker (a conversation-/agent-family span name or a delegate
metadata flag), then rewrites each trace as one `invoke_agent openhands` root
with reparented `chat <model>` and `execute_tool <tool>` children. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

### LLM span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `gen_ai.request.model` | `gen_ai.request.model` | mapped (also names the span) |
| `gen_ai.system` | `gen_ai.provider.name` | mapped |
| `gen_ai.usage.input_tokens` | `gen_ai.usage.input_tokens` | mapped |
| `gen_ai.usage.output_tokens` | `gen_ai.usage.output_tokens` | mapped |
| `llm.usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped |
| `gen_ai.usage.cache_read_input_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped |
| `gen_ai.usage.cache_creation_input_tokens` | `gen_ai.usage.cache_creation.input_tokens` | mapped |
| `gen_ai.input.messages`, `lmnr.span.input` | — | dropped (prompt/response content never leaves the edge) |
| any duration field | — | not provided (no wire duration ⇒ no TTFT source) |

### conversation span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `lmnr.association.properties.session_id` | `gen_ai.conversation.id` | mapped |
| `lmnr.association.properties.user_id` | (`enduser.pseudo.id`) | dropped (user identity outside the vocabulary) |
| `lmnr.association.properties.tags` → `coding_agent.openhands.tags` | — | dropped |
| `conversation.tags.<key>` → `coding_agent.openhands.tag.<key>` | — | dropped |
| `…metadata.is_delegate` | (`coding_agent.openhands.delegate` flag) | dropped (but see claiming below) |
| `…metadata.task_id` → `coding_agent.openhands.delegate.task_id` | — | dropped |
| `…metadata.subagent_type` → `coding_agent.openhands.delegate.subagent_type` | — | dropped |
| `…metadata.parent_session_id` → `coding_agent.openhands.delegate.parent_session_id` | — | dropped |
| other structural intermediates (`conversation.run`, `agent.step`, …) | — | dropped (their timing folds into the root's start/end bounds) |

**Delegate linkage note.** The delegate metadata no longer reaches output as
attributes, but `…metadata.is_delegate = true` still *claims* groups for this
edge, and `…metadata.tool_call_id` still drives the call/result dedupe
internally. Severed sibling fragments stay reconcilable downstream via the
preserved trace/conversation IDs; the linkage detail itself (task ID,
subagent type, parent session) is recoverable only through a raw-preservation
branch.

## Connector-written attributes

These are written by the connector itself, not remapped from raw keys, and
appear on every emitted span:

- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `openhands` (invoke_agent root)
- `coding_agent.source` = `native`
- `coding_agent.client.name` = `openhands`
- `coding_agent.source.scope` = `lmnr.tracer`
- `gen_ai.tool.name` (execute_tool children, from the wire span name)

## Canonical keys with no OpenHands source

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (the wire carries no durations) |
| `gen_ai.response.finish_reasons` | not provided |
| `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `gen_ai.usage.reasoning.output_tokens` | not provided |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `server.address` / `server.port` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `exception.*` | not provided |
