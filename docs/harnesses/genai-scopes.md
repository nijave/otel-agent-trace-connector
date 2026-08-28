# GenAI-semconv emitters — raw → canonical attribute matrix

The GenAI edge claims groups whose scopes match the prefixes
`opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`,
`strands.telemetry`, and `github.copilot`. Most keys from these emitters
arrive already canonical; the edge remaps the few divergent spellings
(`gen_ai.system`, Strands' underscore cache counters, the pre-rename
`cache_creation` dotted spelling) and strips everything
outside the vocabulary. Scopes that do not match the prefixes drop from a
claimed group — spans on unmatched scopes riding along in the same resource
group never reach canonical output; the raw pipelines preserve the
originals.
See [canonical attributes](../canonical-attributes.md) for the
shared vocabulary and the policy behind it.

Status is one of **kept** (already canonical, passes through), **mapped**
(remapped onto the canonical key), **dropped** (deliberately removed;
recoverable only via a raw preservation pipeline branch), or **not provided**
(the source never emits a raw key that would map there). Kept and mapped rows
carry a parenthetical naming any wire condition under which the key is
absent. Kept keys appear only on the spans the producer stamps — this edge
never synthesizes values.

## opentelemetry-instrumentation-openai_v2

Scope `opentelemetry.instrumentation.openai_v2`. Upstream renamed the
package to `opentelemetry-instrumentation-genai-openai` (the old package now
receives only security patches), but the renamed package emits through the
util-genai handler scope in the next section — this scope covers only the
deprecated package.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.operation.name` | all | `gen_ai.operation.name` | kept |
| `gen_ai.provider.name` | all (experimental mode) | `gen_ai.provider.name` | kept (default semconv-v1.30.0 mode emits legacy `gen_ai.system` instead — see the next row) |
| `gen_ai.system` | all (default mode) | `gen_ai.provider.name` | mapped (copied when the dotted key is absent, then removed; a span carrying neither emits no provider) |
| `gen_ai.request.model` | chat | `gen_ai.request.model` | kept |
| `gen_ai.request.max_tokens` | chat | `gen_ai.request.max_tokens` | kept (only when the caller sets `max_tokens`) |
| `gen_ai.response.finish_reasons` | chat | `gen_ai.response.finish_reasons` | kept |
| `gen_ai.response.id` | chat | `gen_ai.response.id` | kept |
| `gen_ai.response.model` | chat | `gen_ai.response.model` | kept |
| `gen_ai.usage.input_tokens` | chat | `gen_ai.usage.input_tokens` | kept (absent for streamed chat completions without `stream_options.include_usage`; the edge never invents counts) |
| `gen_ai.usage.output_tokens` | chat | `gen_ai.usage.output_tokens` | kept (same streaming condition) |
| `server.address` / `server.port` | chat | `server.address` / `server.port` | kept (`server.port` absent for default-port HTTPS endpoints) |
| TTFT (either spelling) | chat | — | not provided (metric-only: openai-v2 emits TTFT as a metric histogram, never as a span attribute) |
| `gen_ai.usage.total_tokens` | chat | `gen_ai.usage.total_tokens` | not provided |
| `gen_ai.usage.cache_read.input_tokens` / `cache_creation` / `cache_write` | chat | — | not provided |
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
| `gen_ai.agent.tools` | invoke_agent | — | dropped (tool definitions are content behind the capture opt-in) |
| `gen_ai.tool.description` | execute_tool | — | dropped (content) |
| `gen_ai.tool.json_schema` | execute_tool | — | dropped (content) |
| `gen_ai.input.messages` / `gen_ai.output.messages` | all | — | dropped (content; log-event mode never reaches this edge) |

## strands.telemetry

Scope `strands.telemetry.tracer`. Strands exports prompt/completion content
as span events by default; this edge strips those events.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.system` | all | `gen_ai.provider.name` | mapped (value e.g. `strands-agents`) |
| `gen_ai.usage.input_tokens` / `output_tokens` | chat, invoke_agent | same | kept (a chat span that ends in an exception carries no usage keys at all) |
| `gen_ai.usage.prompt_tokens` | chat, invoke_agent | `gen_ai.usage.input_tokens` | dropped (duplicate of the current key; dedupe maps it only when absent, then removes it) |
| `gen_ai.usage.completion_tokens` | chat, invoke_agent | `gen_ai.usage.output_tokens` | dropped (same dedupe) |
| `gen_ai.usage.total_tokens` | chat, invoke_agent | `gen_ai.usage.total_tokens` | kept (same exception condition) |
| `gen_ai.usage.cache_read_input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_read.input_tokens` | mapped (underscore variant; same exception condition) |
| `gen_ai.usage.cache_write_input_tokens` | invoke_agent | `gen_ai.usage.cache_write.input_tokens` | mapped (underscore variant; invoke_agent only on the wire, so chat spans never carry the canonical key) |
| `gen_ai.server.time_to_first_token` | chat | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; the legacy key drops after the copy; absent when the chat ends in an exception) |
| `gen_ai.event.start_time` / `end_time` | all | same | kept |
| `gen_ai.request.model` | chat, invoke_agent | `gen_ai.request.model` | kept |
| `gen_ai.agent.name` | invoke_agent | `gen_ai.agent.name` | kept |
| `gen_ai.tool.call.id` / `gen_ai.tool.name` / `gen_ai.tool.status` | execute_tool | same | kept |
| `event_loop.cycle_id` / `parent_cycle_id` | execute_event_loop_cycle | — | dropped |
| `gen_ai.agent.tools` | invoke_agent | — | dropped (content) |
| `gen_ai.tool.description` / `json_schema` | execute_tool | — | dropped (content) |
| `gen_ai.usage.reasoning.output_tokens` | — | `gen_ai.usage.reasoning.output_tokens` | not provided |

Span-name renames: `chat` gains the model suffix (`chat <model>`) when the
model attribute is present; `invoke_agent` likewise renames by agent name.
Other operations keep their wire names. The rename rule applies to every
family in this edge, Copilot included — a span without its subject attribute
keeps the bare wire name.

## github.copilot

Scope `github.copilot` (prefix match tolerates sub-scopes). Covers GitHub
Copilot CLI and VS Code Chat extensions.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.provider.name` | all | `gen_ai.provider.name` | kept (per-span: Copilot omits both it and legacy `gen_ai.system` on tool/hook spans and some invoke_agent spans, which then emit no provider) |
| `gen_ai.conversation.id` | all | `gen_ai.conversation.id` | kept (only where the producer stamps it; absent on execute_tool and execute_hook) |
| `gen_ai.agent.name` | invoke_agent | `gen_ai.agent.name` | kept (the rename subject for `invoke_agent <agent>`; the BYOK CLI capture omits it, leaving the bare `invoke_agent` name) |
| `gen_ai.request.model` | chat, invoke_agent | same | kept |
| `gen_ai.request.stream` | chat | same | kept (chat only; absent entirely in the VS Code Chat capture) |
| `gen_ai.response.finish_reasons` | chat, invoke_agent | same | kept |
| `gen_ai.response.id` / `model` | chat | same | kept |
| `gen_ai.response.time_to_first_chunk` | chat | `gen_ai.response.time_to_first_chunk` | kept (seconds, double) |
| `gen_ai.usage.input_tokens` / `output_tokens` | chat, invoke_agent | same | kept |
| `gen_ai.usage.cache_read.input_tokens` | chat, invoke_agent | same | kept (only on requests that hit prompt cache; a session's first turn carries none) |
| `gen_ai.usage.cache_creation.input_tokens` / `cache_write.input_tokens` | invoke_agent | `gen_ai.usage.cache_write.input_tokens` | mapped (the pre-rename `cache_creation` spelling remaps onto the current registry key; a raw `cache_write` spelling already matches canonical and passes through; absent when the wire reports no cache writes) |
| `gen_ai.usage.reasoning.output_tokens` | chat, invoke_agent | same | kept (only when the model reports reasoning tokens for that turn) |
| `gen_ai.agent.id` / `agent.version` | invoke_agent | same | kept (e.g. `github.copilot.default`) |
| `gen_ai.tool.call.id` / `gen_ai.tool.name` / `gen_ai.tool.type` | execute_tool | same | kept |
| `server.address` / `server.port` | chat, invoke_agent | same | kept (absent entirely in the VS Code Chat capture) |
| `github.copilot.cost` | chat, invoke_agent | — | dropped |
| `github.copilot.aiu` | invoke_agent | — | dropped |
| `github.copilot.turn_id` / `interaction_id` | chat | — | dropped |
| `github.copilot.server_duration` | chat | — | dropped |
| `github.copilot.current_tokens` / `token_limit` / `messages_length` / `turn_count` | invoke_agent | — | dropped (session bookkeeping, not per-response token accounting) |
| `github.copilot.git.branch` / `repository` / `commit_sha` | invoke_agent | — | dropped (infrastructure identifiers, not operational signal) |
| `github.copilot.context.custom_agent_names` / `mcp_server_names` / `skills` | invoke_agent | — | dropped |
| `github.copilot.agent.type` | invoke_agent | — | dropped (`gen_ai.agent.id` carries the identity) |
| `github.copilot.hook.decision` | execute_hook | — | dropped |
| `github.copilot.initiator` | chat | — | dropped |
| `copilot_chat.repo.remote_url` | invoke_agent | — | dropped (legacy namespace) |
| `enduser.pseudo.id` | all | — | dropped |
| `github.copilot.user.message` / `session.usage_info` / `session.shutdown` | events | event names survive, attributes stripped | kept as bare events |

## Canonical usage keys with no GenAI-edge source

Across all four emitters there is no raw source for:

| Canonical key | Emitter | Status |
|---|---|---|
| `gen_ai.usage.total_tokens` | openai_v2, util-genai, copilot | not provided (Strands provides it) |
| `gen_ai.usage.cache_write.input_tokens` | openai_v2, util-genai | not provided (Strands provides the underscore variant; Copilot emits the pre-rename spelling natively on invoke_agent) |
| `gen_ai.usage.reasoning.output_tokens` | openai_v2, util-genai, strands | not provided (Copilot provides it) |
| `gen_ai.response.time_to_first_chunk` | openai_v2, util-genai | not provided in traces (openai-v2 reports TTFT only as a metric); Strands provides it via the mapped legacy server key; Copilot CLI provides it directly (seconds, double) |

## Connector-written attributes

The connector writes these itself on every claimed span that carries
`gen_ai.operation.name`; a matched-scope span without that key passes
through with content stripped but gains no canonical keys:

- `coding_agent.source` = `native`
- `coding_agent.source.scope` = original scope name
- `coding_agent.client.name` = resource `service.name`
- `coding_agent.client.version` = resource `service.version` (when present)
