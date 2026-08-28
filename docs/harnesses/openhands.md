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

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

### LLM span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `gen_ai.request.model` | `gen_ai.request.model` | mapped (also names the span; absent — and the span named bare `chat` — when the wire span carries no model) |
| `gen_ai.system` | `gen_ai.provider.name` | mapped (absent when the wire omits `gen_ai.system`, as LiteLLM responses-style spans do) |
| `gen_ai.usage.input_tokens` | `gen_ai.usage.input_tokens` | mapped (absent for streamed completions, which carry no usage upstream) |
| `gen_ai.usage.output_tokens` | `gen_ai.usage.output_tokens` | mapped (absent for streamed completions, which carry no usage upstream) |
| `llm.usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped (absent when the wire omits it; responses-style spans report only input/output) |
| `gen_ai.usage.cache_read_input_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (absent when the request did no prompt caching) |
| `gen_ai.usage.cache_creation_input_tokens` | `gen_ai.usage.cache_write.input_tokens` | mapped (absent when the request did no prompt caching) |
| `gen_ai.input.messages`, `lmnr.span.input` | — | dropped (prompt/response content never leaves the edge) |
| any duration field | — | not provided (no wire duration ⇒ no TTFT source) |

### TOOL span → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| span name | `gen_ai.tool.name` (also names the span) | mapped |
| `gen_ai.tool.description` | — | dropped |

### conversation span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `lmnr.association.properties.session_id` | `gen_ai.conversation.id` | mapped (a mid-conversation fragment's synthetic root inherits it from kept children, and lacks it when no kept child carries `session_id` — in the pinned wire only `conversation` spans do) |
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

The connector writes these itself rather than remapping them from raw keys;
they appear on every emitted span except where noted:

- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `openhands` (invoke_agent root only)
- `coding_agent.source` = `native`
- `coding_agent.client.name` = `openhands`
- `coding_agent.source.scope` = `lmnr.tracer` (invoke_agent root only)
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
