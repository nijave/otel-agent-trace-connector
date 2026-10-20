# GenAI-semconv emitters — OpenTelemetry signal reference

The GenAI edge claims groups whose scopes match the prefixes
`opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`,
`strands.telemetry`, and `github.copilot`. Most keys from these emitters
arrive already canonical; the edge remaps the few divergent spellings
(`gen_ai.system`, legacy prompt/completion token counts, Strands' underscore
cache counters and millisecond TTFT key, the pre-rename `cache_creation`
dotted spelling) and strips everything outside the vocabulary. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Signal support

| Signal  | openai-v2 / util-genai | strands.telemetry | github.copilot |
| ------- | ----------------------- | ------------------ | --------------- |
| Traces  | native | native | native |
| Logs    | opt-in `gen_ai.client.inference.operation.details` log events | none | CLI: none; VS Code Chat: unbadged "events" |
| Metrics | native (GenAI-semconv histograms) | native (in-tree `MetricsClient`) | native (both surfaces) |

Connector support for all three emitter families: traces edge (see
[docs/otel-signals.md](../otel-signals.md)). Last verified: Traces, Logs and
Metrics 2026-10-19 (Copilot CLI 1.0.100, strands-agents 1.61.1,
opentelemetry-instrumentation-openai-v2 2.4b0 with opentelemetry-util-genai
1.1b0; see Sources).

## Traces

### Native support

All four emitters put native OTel traces on the wire without a plugin or
extension. `opentelemetry-instrumentation-openai_v2` and
`opentelemetry-util-genai` emit standard GenAI-semconv spans from any Python
app instrumenting the OpenAI SDK. `strands.telemetry.tracer` emits spans from
the Strands Agents SDK's own tracer. `github.copilot` covers both Copilot CLI
(OTel export since v1.0.4) and VS Code Copilot Chat, each stamping
`invoke_agent`/`chat`/`execute_tool` spans; VS Code Copilot Chat adds
`execute_hook` spans, while Copilot CLI records hooks as span events since
v1.0.12.

### Connector mapping

Status is one of **kept** (already canonical, passes through), **mapped**
(remapped onto the canonical key), **dropped** (deliberately removed;
recoverable only via a raw preservation pipeline branch), or **not provided**
(the source never emits a raw key that would map there). Kept and mapped rows
carry a parenthetical naming any wire condition under which the key is
absent. Kept keys appear only on the spans the producer stamps — this edge
never synthesizes values. Scopes that do not match the four prefixes drop
from a claimed group — spans on unmatched scopes riding along in the same
resource group never reach canonical output; the raw pipelines preserve the
originals.

#### opentelemetry-instrumentation-openai_v2

Scope `opentelemetry.instrumentation.openai_v2`. Upstream renamed the
package to `opentelemetry-instrumentation-genai-openai`, developed in
`open-telemetry/opentelemetry-python-genai` (the copy in
opentelemetry-python-contrib now receives only security patches); the
renamed package emits through the util-genai handler scope in the next
section, so this scope covers only the old package.

Even the old package now emits chat spans on the util-genai scope:
opentelemetry-util-genai 1.0b0 removed the `OTEL_SEMCONV_STABILITY_OPT_IN`
gate, so `is_experimental_mode()` always returns true and openai-v2 2.4b0
always takes its latest-conventions path, routing chat completions through
`TelemetryHandler` (scope `opentelemetry.util.genai.handler`). The pinned
capture shows this: both of its runs arrive on that scope with
`gen_ai.provider.name` and no `gen_ai.system` — the second run set the opt-in
and still landed identically, which is why the e2e stack now runs the agent
once, as `openai-adhoc`. With util-genai
≥1.0b0 this scope carries only embeddings spans. Chat spans on
this scope, stamped with the semconv v1.30.0 legacy `gen_ai.system`, need
util-genai older than 1.0b0 without the opt-in.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.operation.name` | all | `gen_ai.operation.name` | kept |
| `gen_ai.provider.name` | all | `gen_ai.provider.name` | kept (absent on legacy-mode spans, which emit `gen_ai.system` instead — see the next row) |
| `gen_ai.system` | all (legacy mode: util-genai <1.0b0 without the opt-in) | `gen_ai.provider.name` | mapped (copied when the dotted key is absent, then removed; a span carrying neither emits no provider) |
| `gen_ai.request.model` | chat, embeddings | `gen_ai.request.model` | kept (absent when the call passes no model) |
| `gen_ai.request.max_tokens` | chat | `gen_ai.request.max_tokens` | kept (only when the caller sets `max_tokens`) |
| `gen_ai.response.finish_reasons` | chat | `gen_ai.response.finish_reasons` | kept |
| `gen_ai.response.id` | chat | `gen_ai.response.id` | kept |
| `gen_ai.response.model` | chat, embeddings | `gen_ai.response.model` | kept |
| `gen_ai.usage.input_tokens` | chat, embeddings | `gen_ai.usage.input_tokens` | kept (absent for streamed chat completions without `stream_options.include_usage`; the edge never invents counts) |
| `gen_ai.usage.output_tokens` | chat | `gen_ai.usage.output_tokens` | kept (same streaming condition) |
| `server.address` / `server.port` | chat, embeddings | `server.address` / `server.port` | kept (absent when the OpenAI client's `base_url` is neither an `httpx.URL` nor a string; `server.port` also absent for port 443) |
| TTFT (either spelling) | chat | — | not provided (openai-v2 2.4b0 records no TTFT on spans or metrics; its stream wrapper never reports chunks to util-genai) |
| `gen_ai.usage.total_tokens` | chat | `gen_ai.usage.total_tokens` | not provided |
| `gen_ai.usage.cache_read.input_tokens` / `cache_creation` / `cache_write` | chat | — | not provided |
| `gen_ai.usage.reasoning.output_tokens` | chat | — | not provided |

#### opentelemetry-util-genai

Scope `opentelemetry.util.genai.handler` (and sub-scopes below
`opentelemetry.util.genai`). Spans emitted through `TelemetryHandler` follow
semconv v1.37.0. openai-v2 2.4b0 chat spans arrive here with the chat keys
listed above. The pinned capture carries none of `server.address` or
`server.port`, against openai 3.35.1. Direct util-genai users can also set
the keys below:

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.request.stream` | chat | same | kept (only when the instrumentation uses util-genai's shared stream wrappers; openai-v2 2.4b0 does not) |
| `gen_ai.response.time_to_first_chunk` | chat | same | kept (same stream-wrapper condition) |
| `gen_ai.usage.cache_read.input_tokens` | chat, invoke_agent | same | kept (only when the instrumentation sets it; openai-v2 2.4b0 does not) |
| `gen_ai.usage.cache_creation.input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_write.input_tokens` | mapped (pre-rename spelling from semconv 0.65b0; same instrumentation condition) |
| `gen_ai.usage.reasoning.output_tokens` | chat | same | kept (same instrumentation condition) |
| `gen_ai.agent.id` / `name` / `version`, `gen_ai.conversation.id` | invoke_agent | same | kept (only when the caller sets them) |
| `gen_ai.tool.name` / `gen_ai.tool.call.id` / `gen_ai.tool.type` | execute_tool | same | kept (only when the caller sets them) |
| `gen_ai.system` | all | `gen_ai.provider.name` | mapped (util-genai 1.1b0 never emits it; covers older emitters; removed after copy, and the dotted key wins when already present) |
| `gen_ai.usage.prompt_tokens` | chat | `gen_ai.usage.input_tokens` | mapped (util-genai 1.1b0 never emits it; dedupe: only when the current key is absent) |
| `gen_ai.usage.completion_tokens` | chat | `gen_ai.usage.output_tokens` | mapped (same; dedupe: only when the current key is absent) |
| `gen_ai.tool.description` | execute_tool | — | dropped |
| `gen_ai.tool.definitions` / `gen_ai.system_instructions` | invoke_agent, chat | — | dropped (content behind the capture opt-in) |
| `gen_ai.tool.call.arguments` / `gen_ai.tool.call.result` | execute_tool | — | dropped (content) |
| `gen_ai.input.messages` / `gen_ai.output.messages` | all | — | dropped (content; log-event mode never reaches this edge) |

#### strands.telemetry

Scope `strands.telemetry.tracer`. Strands exports prompt/completion content
as span events by default; this edge strips those events.

Strands chooses its spelling with `OTEL_SEMCONV_STABILITY_OPT_IN`. By
default it stamps `gen_ai.system`. With `gen_ai_latest_experimental` it
stamps `gen_ai.provider.name` instead, which passes through, and it stops
dual-emitting the underscore cache variants.

Prompt-cache usage: since 1.54.0 (sdk-python#3964) the tracer stamps the
dotted spellings (`cache_read.input_tokens`, `cache_creation.input_tokens`)
and, in default mode, also the underscore variants
(`cache_read_input_tokens`, `cache_write_input_tokens`). The pinned 1.61.1
capture is the first fixture with all four, on `chat` and `invoke_agent`
alike. The canonical spellings land as they are, the underscore and
pre-rename variants remap onto them, and no duplicate survives.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.system` | all | `gen_ai.provider.name` | mapped (value e.g. `strands-agents`; absent under `gen_ai_latest_experimental`, where `gen_ai.provider.name` arrives directly and passes through) |
| `gen_ai.usage.input_tokens` / `output_tokens` | chat, invoke_agent | same | kept (a chat span that ends in an exception carries no usage keys at all) |
| `gen_ai.usage.prompt_tokens` | chat, invoke_agent | `gen_ai.usage.input_tokens` | dropped (duplicate of the current key; dedupe maps it only when absent, then removes it) |
| `gen_ai.usage.completion_tokens` | chat, invoke_agent | `gen_ai.usage.output_tokens` | dropped (same dedupe) |
| `gen_ai.usage.total_tokens` | chat, invoke_agent | `gen_ai.usage.total_tokens` | kept (same exception condition) |
| `gen_ai.usage.cache_read.input_tokens` | chat, invoke_agent | same | kept (on `chat` only when the model reports cache-read usage; `invoke_agent` always carries it, defaulting to 0; the underscore duplicate drops) |
| `gen_ai.usage.cache_read_input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_read.input_tokens` | mapped (underscore variant, default mode only; dedupe maps it only when the canonical spelling is absent, which never happens in 1.61.1) |
| `gen_ai.usage.cache_write_input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_write.input_tokens` | mapped (underscore variant, default mode only; Strands never sends the canonical spelling, so in default mode this variant populates the key) |
| `gen_ai.usage.cache_creation.input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_write.input_tokens` | mapped (pre-rename dotted spelling; remaps only when the canonical key is absent, then drops — the underscore write variant fills it first in default mode; under `gen_ai_latest_experimental` this is the only source; on `chat` only when the model reports cache-write usage) |
| `gen_ai.server.time_to_first_token` | chat | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; the legacy key drops after the copy; absent when the chat ends in an exception or the model reports no time-to-first-byte) |
| `gen_ai.server.request.duration` | chat | — | dropped (only when the model reports latency; absent in the pinned capture) |
| `gen_ai.event.start_time` / `end_time` | all | same | kept |
| `gen_ai.request.model` | chat, invoke_agent | `gen_ai.request.model` | kept |
| `gen_ai.agent.name` | invoke_agent | `gen_ai.agent.name` | kept |
| `gen_ai.tool.call.id` / `gen_ai.tool.name` / `gen_ai.tool.status` | execute_tool | same | kept |
| `event_loop.cycle_id` / `parent_cycle_id` | execute_event_loop_cycle | — | dropped |
| `gen_ai.agent.tools` | invoke_agent | — | dropped (content) |
| `gen_ai.tool.description` / `json_schema` | execute_tool | — | dropped (content) |
| `gen_ai.usage.reasoning.output_tokens` | — | `gen_ai.usage.reasoning.output_tokens` | not provided |

Span-name renames: `chat` gains the model suffix (`chat <model>`) when the
model attribute is present; `invoke_agent` renames by agent name and
`execute_tool` by tool name (`execute_tool <tool>`). Other operations
(`execute_event_loop_cycle`, `execute_hook`, `embeddings`) keep their wire
names. The rename rule applies to every family in this edge, Copilot
included — the pinned CLI capture's `execute_tool bash bash` becomes
`execute_tool bash` — and a span without its subject attribute keeps the bare
wire name.

#### github.copilot

Scope `github.copilot` (prefix match tolerates sub-scopes). Covers GitHub
Copilot CLI and VS Code Chat extensions. Copilot CLI lets
`COPILOT_OTEL_SOURCE_NAME` override the scope name; any value outside the
prefix leaves its spans unclaimed by this edge.

Wire drift: the pinned capture is a Copilot CLI 1.0.100 BYOK run against an
OpenAI-compatible provider. Against the 1.0.80 capture it stopped stamping
`gen_ai.agent.id`, `gen_ai.agent.version`, `gen_ai.request.model`,
`gen_ai.usage.cache_read.input_tokens` and
`gen_ai.usage.reasoning.output_tokens` on the `invoke_agent` root, and it added
`github.copilot.initiator` on `chat` plus `process.executable.name` and
`process.exit.code` on `execute_tool`. All three additions fall outside the
vocabulary, so they drop.

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `gen_ai.provider.name` | all | `gen_ai.provider.name` | kept (absent when the producer omits it: Copilot CLI stamps it on `execute_tool` only when available, and VS Code Copilot Chat documents none on `execute_tool` or `execute_hook`; the pinned 1.0.100 capture carries it on every span) |
| `gen_ai.conversation.id` | all | `gen_ai.conversation.id` | kept (absent when the producer omits it: VS Code Copilot Chat documents it on `invoke_agent` only; the pinned 1.0.100 CLI capture carries it on `invoke_agent`, `chat` and `execute_tool`) |
| `gen_ai.agent.name` | invoke_agent | `gen_ai.agent.name` | kept (the rename subject for `invoke_agent <agent>`; absent when the producer omits it — the pinned 1.0.100 BYOK CLI capture does, leaving the bare `invoke_agent` name) |
| `gen_ai.request.model` | chat, invoke_agent | same | kept (absent on `invoke_agent` when the producer omits it; the pinned 1.0.100 capture carries it on chat only) |
| `gen_ai.request.max_tokens` | chat | same | kept (VS Code Copilot Chat only; Copilot CLI does not send it) |
| `gen_ai.request.stream` | chat | same | kept (Copilot CLI streaming requests only; VS Code Copilot Chat sends no such key) |
| `gen_ai.request.reasoning.level` | chat | same | kept (Copilot CLI since 1.0.64, only when the user sets a reasoning effort; absent in the pinned capture) |
| `gen_ai.response.finish_reasons` | chat, invoke_agent | same | kept |
| `gen_ai.response.id` / `model` | chat | same | kept (VS Code Copilot Chat also stamps `gen_ai.response.model` on `invoke_agent`) |
| `gen_ai.response.time_to_first_chunk` | chat | `gen_ai.response.time_to_first_chunk` | kept (seconds, double; Copilot CLI streaming requests only; VS Code Copilot Chat sends `copilot_chat.time_to_first_token` instead, which drops) |
| `gen_ai.usage.input_tokens` / `output_tokens` | chat, invoke_agent | same | kept |
| `gen_ai.usage.cache_read.input_tokens` | chat, invoke_agent | same | kept (only on requests that hit prompt cache; absent on `invoke_agent` in the pinned 1.0.100 capture, whose root carries none) |
| `gen_ai.usage.cache_creation.input_tokens` / `cache_write.input_tokens` | chat, invoke_agent | `gen_ai.usage.cache_write.input_tokens` | mapped (the pre-rename `cache_creation` spelling both surfaces document remaps onto the current registry key; a raw `cache_write` spelling already matches canonical and passes through; absent when the wire reports no cache writes, as in the pinned capture) |
| `gen_ai.usage.reasoning.output_tokens` | chat, invoke_agent | same | kept (only when the model reports reasoning tokens for that turn, and absent on `invoke_agent` when the producer does not sum them — the pinned 1.0.100 capture carries it on chat only) |
| `gen_ai.usage.reasoning_tokens` | chat | — | dropped (VS Code Copilot Chat legacy spelling, dual-emitted beside `gen_ai.usage.reasoning.output_tokens`; not a canonical usage key) |
| `gen_ai.agent.id` / `agent.version` | invoke_agent | same | kept (absent when the producer does not stamp them: the pinned 1.0.100 CLI capture omits both, 1.0.80 stamped `github.copilot.default`) |
| `gen_ai.agent.description` | invoke_agent | — | dropped |
| `gen_ai.tool.call.id` / `gen_ai.tool.name` / `gen_ai.tool.type` | execute_tool | same | kept |
| `gen_ai.tool.description` | execute_tool | — | dropped |
| `server.address` / `server.port` | chat, invoke_agent | same | kept (Copilot CLI omits both on subagent `invoke_agent` spans; VS Code Copilot Chat sends only `server.address`, on `chat`) |
| `github.copilot.cost` | chat, invoke_agent | — | dropped |
| `github.copilot.nano_aiu` | chat, invoke_agent | — | dropped |
| `github.copilot.turn_id` / `interaction_id` | chat | — | dropped |
| `github.copilot.server_duration` | chat | — | dropped |
| `github.copilot.turn_count` | invoke_agent | — | dropped (session bookkeeping, not per-response token accounting) |
| `github.copilot.git.branch` / `repository` / `commit_sha`, `github.copilot.github.org` | invoke_agent | — | dropped (infrastructure identifiers, not operational signal) |
| `github.copilot.context.custom_agent_names` / `mcp_server_names` / `skills` | invoke_agent | — | dropped |
| `github.copilot.agent.type` | invoke_agent | — | dropped (`builtin`, `custom`, or `plugin`) |
| `github.copilot.tool.parameters.*` | execute_tool | — | dropped (VS Code Copilot Chat tool parameters; some are content) |
| `github.copilot.hook.decision` / `duration` / `tool_names` | execute_hook | — | dropped |
| `github.copilot.initiator` | chat | — | dropped (names the turn's initiator — `user` or `agent`) |
| `process.executable.name` / `process.exit.code` | execute_tool | — | dropped (process bookkeeping for the tool call, not tool identity) |
| `copilot_chat.*` | invoke_agent, chat, execute_hook | — | dropped (VS Code Copilot Chat legacy namespace, e.g. `repo.remote_url`, `turn_count`, `time_to_first_token`, `hook_input`) |
| `user.name` | invoke_agent | — | dropped (VS Code Copilot Chat identity capture) |
| `enduser.pseudo.id` | invoke_agent | `coding_agent.user.id` | mapped (only when `capture_identity` is on; absent on other span types) |
| span events (e.g. `github.copilot.user.message`, `session.usage_info`, `session.shutdown`, `hook.start`, `sandbox.enforcement_state`, `exception`) | invoke_agent, chat, execute_tool | event names survive, attributes stripped to the vocabulary | kept as bare events (`session.usage_info` carries `github.copilot.current_tokens` / `token_limit` / `messages_length`, which drop) |

#### Canonical usage keys with no GenAI-edge source

Across all four emitters there is no raw source for:

| Canonical key | Emitter | Status |
|---|---|---|
| `gen_ai.usage.total_tokens` | openai_v2, util-genai, copilot | not provided (Strands provides it) |
| `gen_ai.usage.cache_write.input_tokens` | openai_v2 | not provided (util-genai and Copilot can emit the pre-rename `cache_creation` spelling, which maps; Strands provides the underscore variant; the pinned Copilot capture carries none) |
| `gen_ai.usage.reasoning.output_tokens` | openai_v2, strands | not provided (Copilot provides it; util-genai can when the instrumentation sets it) |
| `gen_ai.response.time_to_first_chunk` | openai_v2 | not provided (openai-v2 2.4b0 records no TTFT); util-genai provides it through its shared stream wrappers; Strands provides it via the mapped legacy server key; Copilot CLI provides it directly (seconds, double) |

#### Connector-written attributes

The connector writes these itself on every claimed span that carries
`gen_ai.operation.name`; a matched-scope span without that key passes
through with content stripped but gains no canonical keys:

- `coding_agent.source` = `native`
- `coding_agent.source.scope` = original scope name
- `coding_agent.client.name` = resource `service.name`
- `coding_agent.client.version` = resource `service.version` (when present)

## Logs

### Native support

The three emitter families diverge here:

- **openai-v2 / util-genai**: util-genai 1.1b0 emits a
  `gen_ai.client.inference.operation.details` log record per inference when
  `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` is `EVENT_ONLY` or
  `SPAN_AND_EVENT` (or `OTEL_INSTRUMENTATION_GENAI_EMIT_EVENT=true`) and the
  app supplies a `LoggerProvider`; prompt/completion content
  (`gen_ai.input.messages`, `gen_ai.output.messages`) rides those records.
  The default `NO_CONTENT` mode emits none, and the pinned capture has no
  log records. The attribute matrix above already notes this mode "never
  reaches this edge."
- **strands.telemetry**: no OTel logs. The official docs state plainly that
  the Strands SDK uses Python's standard `logging` module (the TypeScript
  SDK likewise uses a plain console/Pino-compatible `Logger` interface) —
  no `LoggerProvider`, no OTel Logs API, no OTLP log exporter anywhere.
  Prompt/completion content instead rides span events
  (`gen_ai.user.message`, `gen_ai.assistant.message`,
  `gen_ai.tool.message`, `gen_ai.choice`, or
  `gen_ai.client.inference.operation.details` under
  `gen_ai_latest_experimental`) or an opt-in span-attribute mode
  (`gen_ai_span_attributes_only`).
- **github.copilot**: Copilot CLI's docs scope OTel export to "traces and
  metrics" only — no standalone logs signal, only span events
  (`github.copilot.hook.start`, `exception`). VS Code Copilot Chat's docs
  describe a third signal called "events"
  (`gen_ai.client.inference.operation.details`,
  `copilot_chat.session.start`, …); the OTel spec implements Events on top
  of the Logs API, but the VS Code docs never call this "logs" or mention
  `OTEL_LOGS_EXPORTER`, so treat it as logs-shaped rather than confirmed
  logs.

### Connector mapping

Not consumed — the GenAI edge normalizes traces only; it does not claim any
emitter's logs or events signal.

## Metrics

### Native support

- **openai-v2 / util-genai**: native GenAI-semconv histograms
  `gen_ai.client.operation.duration` and `gen_ai.client.token.usage` (by
  `gen_ai.token.type`), recorded for chat and embeddings calls alike.
  util-genai 1.1b0 also defines `gen_ai.client.operation.time_to_first_chunk`
  and `gen_ai.client.operation.time_per_output_chunk`, recorded only by
  instrumentations that use its shared stream wrappers; openai-v2 2.4b0 does
  not, so it never records them.
- **strands.telemetry**: native, in-tree `MetricsClient`. Counters
  `strands.event_loop.cycle_count`, `strands.event_loop.start_cycle`/
  `end_cycle`, `strands.tool.call_count`/`success_count`/`error_count`;
  histograms `strands.event_loop.latency`,
  `strands.event_loop.cycle_duration`, `strands.tool.duration`,
  `strands.event_loop.input.tokens`, `strands.event_loop.output.tokens`,
  `strands.event_loop.cache_read.input.tokens`,
  `strands.event_loop.cache_write.input.tokens`,
  `strands.model.time_to_first_token`. The TS SDK uses different instrument
  names: `gen_ai.agent.*` plus `gen_ai.server.time_to_first_token`.
- **github.copilot**: native on both surfaces. Copilot CLI documents
  GenAI-convention histograms (`gen_ai.client.operation.duration`,
  `gen_ai.client.token.usage`,
  `gen_ai.client.operation.time_to_first_chunk`,
  `gen_ai.client.operation.time_per_output_chunk`,
  `gen_ai.invoke_agent.duration`, `gen_ai.invoke_agent.inference_calls`,
  `gen_ai.invoke_agent.tool_calls`, `gen_ai.invoke_workflow.duration`) plus
  `github.copilot.*` vendor metrics (`tool.call.count`, `tool.call.duration`,
  `agent.turn.count`, `mcp.server.connection.count`, `code.lines_added`,
  `code.lines_removed`). VS Code Copilot Chat documents the two GenAI
  histograms (`gen_ai.client.operation.duration`,
  `gen_ai.client.token.usage`) plus `copilot_chat.*` extension metrics.

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines
(`connector/codingagentconnector/factory.go`); it does not process OTel
metrics for any emitter.

## Sources

- GenAI semantic conventions registry (`cache_creation` → `cache_write`
  rename in `changelog.d/440.breaking.md`):
  https://github.com/open-telemetry/semantic-conventions-genai/tree/547934afa90a34298441caf80c43b27255c3a292
  (verified 2026-10-19)
- `opentelemetry-instrumentation-openai-v2` 2.4b0 (`src/.../openai_v2/__init__.py`,
  `patch.py`, `utils.py`, `CHANGELOG.md`):
  https://github.com/open-telemetry/opentelemetry-python-contrib/tree/f691ef4b56a11cbeb772bd0c288e1faf4db757a9/instrumentation-genai/opentelemetry-instrumentation-openai-v2
  (verified 2026-10-19); move notice for the GenAI instrumentations:
  https://github.com/open-telemetry/opentelemetry-python-contrib/blob/main/instrumentation-genai/README.md
  (verified 2026-10-19)
- `opentelemetry-util-genai` 1.1b0 (`README.rst`, `CHANGELOG.md`,
  `src/opentelemetry/util/genai/`; `OTEL_SEMCONV_STABILITY_OPT_IN` removal in
  the 1.0b0 entry, #117):
  https://github.com/open-telemetry/opentelemetry-python-genai/tree/8476682703b0b72f107bc3b58998a77dacc0d1d3/util/opentelemetry-util-genai
  (verified 2026-10-19)
- Strands Agents 1.61.1 telemetry source (`tracer.py`, `metrics.py`,
  `metrics_constants.py`; TS `strands-ts/src/telemetry/meter.ts`):
  https://github.com/strands-agents/sdk-python/tree/5cb880559e5ed88bd55de2779e72d060020f756b/strands-py/src/strands/telemetry
  (verified 2026-10-19); cache-attribute change:
  https://github.com/strands-agents/sdk-python/pull/3964 (first released in
  python/v1.54.0)
- Strands logs docs: https://strandsagents.com/docs/user-guide/sdk/observability-evaluation/logs/
  (verified 2026-10-19)
- GitHub Copilot CLI OpenTelemetry: https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring
  (verified 2026-10-19)
- GitHub Copilot CLI changelog (OTel in 1.0.4, hooks as span events in
  1.0.12, `gen_ai.request.reasoning.level` and dotted usage keys in 1.0.64):
  https://github.com/github/copilot-cli/blob/84a2bd392a397e3dafaa9a1212b2964c3bd694bc/changelog.md
  (verified 2026-10-19)
- VS Code Copilot Chat monitoring: https://code.visualstudio.com/docs/agents/guides/monitoring-agents
  (verified 2026-10-19)
- Traces attribute matrices: the pinned 2026-10-19 e2e captures (Copilot CLI
  1.0.100, strands-agents 1.61.1, openai-v2 2.4b0 with util-genai 1.1b0 and
  openai 3.35.1), committed as `internal/genai/testdata/*-raw.otlp.json`. The
  openai-adhoc capture holds both runs of the stack as it stood that day
  (`openai-adhoc-legacy` and `openai-adhoc-latest`); the stack now runs once as
  `openai-adhoc`, so those two service names live on only in the capture. The
  openai pin is in `e2e/openai-adhoc/requirements.txt` on main
  (https://github.com/nijave/otel-agent-trace-connector/blob/deac347387e1d290f3bfc7b74be2927c6122d066/e2e/openai-adhoc/requirements.txt)
- `docs/otel-signals.md` (refreshed 2026-08-29)
- `docs/metrics.md` (refreshed 2026-08-21)
