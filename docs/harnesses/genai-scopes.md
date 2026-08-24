# GenAI-semconv emitters — raw → canonical attribute matrix

The GenAI edge claims groups whose scopes match the prefixes
`opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`,
`opentelemetry.instrumentation.genai`, `strands.telemetry`, and
`github.copilot`. These emitters mostly produce already-canonical keys; the
edge remaps the few divergent spellings (`gen_ai.system`, Strands' underscore
cache counters) and strips everything outside the vocabulary. Scopes that do
not match the prefixes are dropped from a claimed group — spans on unmatched
scopes riding along in the same resource group never reach canonical output;
the raw pipelines preserve the originals.
See [canonical attributes](../canonical-attributes.md) for the
shared vocabulary and the policy behind it.

Status is one of **kept** (already canonical, passes through), **mapped**
(remapped onto the canonical key), **dropped** (deliberately removed;
recoverable only via a raw preservation pipeline branch), or **not provided**
(the source never emits a raw key that would map there).

## opentelemetry-instrumentation-openai_v2

Scope `opentelemetry.instrumentation.openai_v2` (renaming to
`opentelemetry.instrumentation.genai_openai`; the prefix match tolerates both).

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.operation.name` | all | `gen_ai.operation.name` | kept |
| `gen_ai.provider.name` | all | `gen_ai.provider.name` | kept |
| `gen_ai.request.model` | chat | `gen_ai.request.model` | kept |
| `gen_ai.request.max_tokens` | chat | `gen_ai.request.max_tokens` | kept |
| `gen_ai.response.finish_reasons` | chat | `gen_ai.response.finish_reasons` | kept |
| `gen_ai.response.id` | chat | `gen_ai.response.id` | kept |
| `gen_ai.response.model` | chat | `gen_ai.response.model` | kept |
| `gen_ai.usage.input_tokens` | chat | `gen_ai.usage.input_tokens` | kept |
| `gen_ai.usage.output_tokens` | chat | `gen_ai.usage.output_tokens` | kept |
| `server.address` / `server.port` | chat | `server.address` / `server.port` | kept |
| `gen_ai.server.time_to_first_token` | chat | `gen_ai.response.time_to_first_chunk` | not provided (metric-only: emitted as a metric histogram, never as a span attribute) |
| `gen_ai.usage.total_tokens` | chat | `gen_ai.usage.total_tokens` | not provided |
| `gen_ai.usage.cache_read.input_tokens` / `cache_creation` | chat | — | not provided |
| `gen_ai.usage.reasoning.output_tokens` | chat | — | not provided |

## opentelemetry-util-genai

Scope `opentelemetry.util.genai.handler` (and sub-scopes below
`opentelemetry.util.genai`). Emits the same keys as openai_v2 above; the
differences are:

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.system` | all (legacy mode) | `gen_ai.provider.name` | mapped (removed after copy; the dotted key wins when already present) |
| `gen_ai.usage.prompt_tokens` | chat (legacy mode) | `gen_ai.usage.input_tokens` | mapped (dedupe: only when the current key is absent) |
| `gen_ai.usage.completion_tokens` | chat (legacy mode) | `gen_ai.usage.output_tokens` | mapped (dedupe: only when the current key is absent) |
| `gen_ai.agent.tools` | invoke_agent | — | dropped (tool definitions are capture-gated content) |
| `gen_ai.tool.description` | execute_tool | — | dropped (content) |
| `gen_ai.tool.json_schema` | execute_tool | — | dropped (content) |
| `gen_ai.input.messages` / `gen_ai.output.messages` | all | — | dropped (content; log-event mode never reaches this edge) |

## strands.telemetry

Scope `strands.telemetry.tracer`. Strands exports prompt/completion content
as span events by default; those events are stripped here.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.system` | all | `gen_ai.provider.name` | mapped (value e.g. `strands-agents`) |
| `gen_ai.usage.input_tokens` / `output_tokens` | chat, invoke_agent | same | kept |
| `gen_ai.usage.prompt_tokens` | chat, invoke_agent | `gen_ai.usage.input_tokens` | dropped (duplicate of the current key; dedupe maps it only when absent, then removes it) |
| `gen_ai.usage.completion_tokens` | chat, invoke_agent | `gen_ai.usage.output_tokens` | dropped (same dedupe) |
| `gen_ai.usage.total_tokens` | chat, invoke_agent | `gen_ai.usage.total_tokens` | kept |
| `gen_ai.usage.cache_read_input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_read.input_tokens` | mapped (underscore variant) |
| `gen_ai.usage.cache_write_input_tokens` | invoke_agent | `gen_ai.usage.cache_creation.input_tokens` | mapped (underscore variant) |
| `gen_ai.server.time_to_first_token` | chat | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; the legacy key is removed) |
| `gen_ai.event.start_time` / `end_time` | all | same | kept |
| `gen_ai.request.model` | chat, invoke_agent | `gen_ai.request.model` | kept |
| `gen_ai.agent.name` | invoke_agent | `gen_ai.agent.name` | kept |
| `gen_ai.tool.call.id` / `name` / `status` | execute_tool | same | kept |
| `event_loop.cycle_id` / `parent_cycle_id` | execute_event_loop_cycle | — | dropped |
| `gen_ai.agent.tools` | invoke_agent | — | dropped (content) |
| `gen_ai.tool.description` / `json_schema` | execute_tool | — | dropped (content) |
| `gen_ai.usage.reasoning.output_tokens` | — | `gen_ai.usage.reasoning.output_tokens` | not provided |

Span-name renames: `chat` gains the model suffix (`chat <model>`) when the
model attribute is present; `invoke_agent` likewise renames by agent name.
Other operations keep their wire names.

## github.copilot

Scope `github.copilot` (prefix match tolerates sub-scopes). Covers GitHub
Copilot CLI and VS Code Chat extensions.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.provider.name` | all | `gen_ai.provider.name` | kept |
| `gen_ai.conversation.id` | all | `gen_ai.conversation.id` | kept |
| `gen_ai.request.model` / `request.stream` | chat, invoke_agent | same | kept |
| `gen_ai.response.finish_reasons` / `id` / `model` | chat | same | kept |
| `gen_ai.response.time_to_first_chunk` | chat | `gen_ai.response.time_to_first_chunk` | kept (seconds, double) |
| `gen_ai.usage.input_tokens` / `output_tokens` | chat, invoke_agent | same | kept |
| `gen_ai.usage.cache_read.input_tokens` | chat, invoke_agent | same | kept |
| `gen_ai.usage.reasoning.output_tokens` | chat, invoke_agent | same | kept |
| `gen_ai.agent.id` / `agent.version` | invoke_agent | same | kept (e.g. `github.copilot.default`) |
| `gen_ai.tool.call.id` / `name` / `type` | execute_tool | same | kept |
| `server.address` / `server.port` | chat, invoke_agent | same | kept |
| `github.copilot.cost` | chat, invoke_agent | — | dropped |
| `github.copilot.aiu` | invoke_agent | — | dropped |
| `github.copilot.turn_id` / `interaction_id` | chat | — | dropped |
| `github.copilot.server_duration` | chat | — | dropped |
| `github.copilot.current_tokens` / `token_limit` / `messages_length` / `turn_count` | invoke_agent | — | dropped (session bookkeeping, not per-response token accounting) |
| `github.copilot.git.branch` / `repository` / `commit_sha` | invoke_agent | — | dropped (infrastructure identifiers, not operational signal) |
| `github.copilot.context.custom_agent_names` / `mcp_server_names` / `skills` | invoke_agent | — | dropped |
| `github.copilot.agent.type` | invoke_agent | — | dropped (`gen_ai.agent.id` carries the identity) |
| `github.copilot.hook.decision` | execute_hook | — | dropped |
| `copilot_chat.repo.remote_url` | invoke_agent | — | dropped (legacy namespace) |
| `enduser.pseudo.id` | all | — | dropped |
| `github.copilot.user.message` / `session.usage_info` / `session.shutdown` | events | event names survive, attributes stripped | kept as bare events |

## Canonical usage keys with no GenAI-edge source

Across all four emitters there is no raw source for:

| Canonical key | Emitter | Status |
|---|---|---|
| `gen_ai.usage.total_tokens` | openai_v2, util-genai, copilot-cli | not provided (Strands provides it) |
| `gen_ai.usage.cache_creation.input_tokens` | openai_v2, util-genai, copilot-cli | not provided (Strands provides it via the underscore variant) |
| `gen_ai.usage.reasoning.output_tokens` | openai_v2, util-genai, strands | not provided (Copilot provides it) |
| `gen_ai.response.time_to_first_chunk` | openai_v2, util-genai | not provided in traces (openai-v2 reports TTFT only as a metric); Strands provides it via the mapped legacy server key; Copilot CLI provides it directly (seconds, double) |

## Connector-written attributes

Written by the connector itself on every claimed span, not remapped:

- `coding_agent.source` = `native`
- `coding_agent.source.scope` = original scope name
- `coding_agent.client.name` = resource `service.name`
- `coding_agent.client.version` = resource `service.version`
