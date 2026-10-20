# OpenHands — OpenTelemetry signal reference

OpenHands (`OpenHands/OpenHands`, formerly All-Hands-AI) runs on the
`software-agent-sdk` (Python), where native OTel lives via the Laminar SDK
(`lmnr`) as the OTel layer. The connector claims only groups carrying an
explicit OpenHands marker (a conversation-/agent-family span name or a
delegate metadata flag), then rewrites each trace as one
`invoke_agent openhands` root with reparented `chat <model>` and
`execute_tool <tool>` children. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | native, via Laminar SDK (`lmnr`), scope `lmnr.tracer` | traces edge — this file's mapping |
| Logs | none | not applicable |
| Metrics | none exported (in-process only) | not applicable |

Last verified: Traces 2026-10-19 (OpenHands SDK 1.56.0, `lmnr` 0.7.67),
Logs 2026-10-19, Metrics 2026-10-19 (see Sources).

## Traces

### Native support

OpenHands emits OTLP **traces** on scope `lmnr.tracer` (Laminar's SDK), with
LiteLLM-style attributes on LLM spans and conversation-level state in
`lmnr.association.properties.*`. The SDK turns tracing on when the
environment sets any of `LMNR_PROJECT_API_KEY`, `OTEL_ENDPOINT`,
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, or `OTEL_EXPORTER_OTLP_ENDPOINT`. The exporter assumes gRPC unless
`OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf` (or
`OTEL_EXPORTER=otlp_http`) selects HTTP
([`laminar.py`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/observability/laminar.py#L28-L33),
[`maybe_init_laminar`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/observability/laminar.py#L155-L180),
v1.56.0, checked 2026-10-19).

The main repo (`OpenHands/OpenHands`) is now Agent Canvas, a TypeScript web
app with an optional Electron desktop build and PostHog analytics
(`posthog-js`), not OTel. Tracing lives entirely in the SDK that Canvas
drives through the agent-server
([README](https://github.com/OpenHands/OpenHands/blob/8d39305538c1d17a30cbe477e57a0e18fcee7d95/README.md),
[`package.json`](https://github.com/OpenHands/OpenHands/blob/8d39305538c1d17a30cbe477e57a0e18fcee7d95/package.json#L50),
checked 2026-10-19).

**Environment reaches the agent-server.** In the local Canvas stack (the
`agent-canvas` command and the Electron app, which imports the same launcher
in-process), the agent-server child starts with Canvas's own process
environment merged in, so OTLP variables set where Canvas starts reach the
SDK
([`spawnService`](https://github.com/OpenHands/OpenHands/blob/8d39305538c1d17a30cbe477e57a0e18fcee7d95/scripts/dev-with-automation.mjs#L668-L673),
[`startStack`](https://github.com/OpenHands/OpenHands/blob/8d39305538c1d17a30cbe477e57a0e18fcee7d95/electron/main.mjs#L668-L673),
checked 2026-10-19). An app launched from a desktop launcher sees the desktop
session's environment, not a shell's.

### Connector mapping

The connector claims only groups carrying an explicit OpenHands marker (a
conversation-/agent-family span name or a delegate metadata flag), then
rewrites each trace as one `invoke_agent openhands` root with reparented
`chat <model>` and `execute_tool <tool>` children.

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

##### LLM span → chat

**Two chat spans per LLM call.** The SDK opens its own `llm.<model>` span
(Laminar span type `LLM`) around each call, and lmnr's LiteLLM
instrumentation nests a `litellm.completion` (or `litellm.responses`) span
under it. The connector rewrites both as `chat` children: the wrapper becomes
a bare `chat` span with only the cache-token counts, and the inner span
becomes `chat <model>` with the full request, response, and usage keys. The
two spans carry the same cache-token values, so summing
`gen_ai.usage.cache_read.input_tokens` or `cache_write.input_tokens` across
all chat spans counts each call twice. Sources:
[`llm_call_span`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/observability/laminar.py#L515-L540),
[`Telemetry._open_span` / `_annotate_span`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/llm/utils/telemetry.py#L304-L345)
(v1.56.0, checked 2026-10-19), and the 2026-10-19 live capture (SDK 1.56.0,
`lmnr` 0.7.67, `openrouter/z-ai/glm-5.3-flash`).

| Raw key | Canonical key | Status |
|---|---|---|
| `gen_ai.request.model` | `gen_ai.request.model` | mapped (also names the span; absent — and the span named bare `chat` — when the wire span carries no model, as the `llm.<model>` wrapper spans do) |
| `gen_ai.request.reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (verbatim; absent when the request sets no reasoning effort, and on the `llm.<model>` wrapper spans) |
| `gen_ai.system` | `gen_ai.provider.name` | mapped (absent on the `llm.<model>` wrapper spans; lmnr 0.7.67 writes it on every `litellm.completion` and `litellm.responses` span) |
| `gen_ai.response.id` | `gen_ai.response.id` | mapped (absent when the wire span carries no provider response id, as the `llm.<model>` wrapper spans do) |
| `gen_ai.response.model` | `gen_ai.response.model` | mapped (absent when the wire span carries no response model, as the `llm.<model>` wrapper spans do) |
| `gen_ai.usage.input_tokens` | `gen_ai.usage.input_tokens` | mapped (absent on the `llm.<model>` wrapper spans, on streamed completions whose chunks carry no usage, and on responses-API calls whose response has no usage) |
| `gen_ai.usage.output_tokens` | `gen_ai.usage.output_tokens` | mapped (absent under the same conditions as `input_tokens`) |
| `llm.usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped (absent under the same conditions as `input_tokens`) |
| `gen_ai.usage.cache_read_input_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (the `llm.<model>` wrapper writes it, `0` when nothing hit the cache, whenever the SDK has usage; the inner LiteLLM span lacks it unless the provider reports prompt-token details) |
| `gen_ai.usage.cache_creation_input_tokens` | `gen_ai.usage.cache_write.input_tokens` | mapped (wrapper as for `cache_read`; the inner span lacks it unless the provider reports prompt-token details, and `litellm.responses` spans never carry it) |
| `gen_ai.request.max_tokens` | — | dropped (connector gap: lmnr writes it when the request sets `max_tokens` or `max_completion_tokens`, which the SDK does when it knows the model's output limit; absent from the 2026-10-19 capture) |
| `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.tool.definitions`, `lmnr.span.input`, `lmnr.span.output` | — | dropped (prompt, response, and tool-definition content never leaves the edge) |
| `lmnr.span.*` bookkeeping (`path`, `parent_path`, `ids_path`, `parent_ids_path`, `type`, `sdk_version`, `language_version`, `instrumentation_source`, `instrumentation_scope.name`, `instrumentation_scope.version`) | — | dropped (Laminar span bookkeeping; the edge reads only `lmnr.span.type`, to classify the span) |
| `gen_ai.usage.cost` | — | dropped (the SDK's per-request dollar cost on the `llm.<model>` wrapper spans; the canonical vocabulary has no cost key) |
| any duration field | — | not provided (no wire duration ⇒ no TTFT source) |

lmnr attribute sources:
[completions](https://github.com/lmnr-ai/lmnr-python/blob/0.7.67/src/lmnr/opentelemetry_lib/opentelemetry/instrumentation/litellm/wrappers/completions/__init__.py#L22-L130),
[completions streaming](https://github.com/lmnr-ai/lmnr-python/blob/0.7.67/src/lmnr/opentelemetry_lib/opentelemetry/instrumentation/litellm/wrappers/completions/streaming.py#L100-L140),
[responses](https://github.com/lmnr-ai/lmnr-python/blob/0.7.67/src/lmnr/opentelemetry_lib/opentelemetry/instrumentation/litellm/wrappers/responses/__init__.py#L25-L122),
and the SDK's
[`select_chat_options`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/llm/options/chat_options.py#L20-L52)
(checked 2026-10-19).

##### TOOL span → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| span name | `gen_ai.tool.name` (also names the span) | mapped |
| `lmnr.association.properties.metadata.tool_call_id` | — | dropped (connector gap: the edge never emits it as `gen_ai.tool.call.id`; it drives the internal call/result dedupe only, and when the wire omits it the edge emits both records) |
| `lmnr.span.input`, `lmnr.span.output` | — | dropped (tool arguments and results never leave the edge) |
| `gen_ai.tool.description` | — | dropped (absent from the 2026-10-19 capture) |

##### conversation span → invoke_agent root

| Raw key | Canonical key | Status |
|---|---|---|
| `lmnr.association.properties.session_id` | `gen_ai.conversation.id` | mapped (a mid-conversation fragment's synthetic root inherits it from kept children, and lacks it only when no kept child carries `session_id`; every span in the 2026-10-19 capture carries it) |
| `lmnr.association.properties.user_id` | `coding_agent.user.id` | mapped (only when `capture_identity` is on; absent when the conversation sets no `user_id`, as in the 2026-10-19 capture) |
| `lmnr.association.properties.tags` → `coding_agent.openhands.tags` | — | dropped |
| `conversation.tags.<key>` → `coding_agent.openhands.tag.<key>` | — | dropped |
| `…metadata.is_delegate` | (`coding_agent.openhands.delegate` flag) | dropped (but see claiming below) |
| `…metadata.task_id` → `coding_agent.openhands.delegate.task_id` | — | dropped |
| `…metadata.subagent_type` → `coding_agent.openhands.delegate.subagent_type` | — | dropped |
| `…metadata.parent_session_id` → `coding_agent.openhands.delegate.parent_session_id` | — | dropped |
| `…metadata.delegate.parent_trace_id`, `…metadata.delegate.parent_span_id` | — | dropped (the severed parent's trace and span IDs) |
| other structural intermediates (`conversation.run`, `agent.step`, …) | — | dropped (their timing folds into the root's start/end bounds) |

**Delegate linkage note.** The delegate metadata no longer reaches output as
attributes, but `…metadata.is_delegate = true` still *claims* groups for this
edge, and `…metadata.tool_call_id` still drives the call/result dedupe
internally. Severed sibling fragments stay reconcilable downstream via the
preserved trace/conversation IDs; the linkage detail itself (task ID,
subagent type, parent session, parent trace and span IDs) is recoverable
only through a raw-preservation branch. The SDK writes these keys in
[`_delegate_observability_metadata`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-tools/openhands/tools/task/manager.py#L347-L365)
and
[`detached_delegate_context`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/observability/laminar.py#L549-L593)
(v1.56.0, checked 2026-10-19).

#### Connector-written attributes

The connector writes these itself rather than remapping them from raw keys;
they appear on every emitted span except where noted:

- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `openhands` (invoke_agent root only)
- `coding_agent.source` = `native`
- `coding_agent.client.name` = `openhands`
- `coding_agent.source.scope` = `lmnr.tracer` (invoke_agent root only)
- `gen_ai.tool.name` (execute_tool children, from the wire span name)

#### Canonical keys with no OpenHands source

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (the wire carries no durations) |
| `gen_ai.response.finish_reasons` | not provided |
| `gen_ai.usage.reasoning.output_tokens` | not provided |
| `gen_ai.request.stream` | not provided |
| `server.address` / `server.port` | not provided |
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `exception.*` | not provided |

## Logs

### Native support

None. OpenHands has no OTel logs signal: the SDK's only OTel setup is
Laminar tracing
([`maybe_init_laminar`](https://github.com/OpenHands/software-agent-sdk/blob/33d8aa29c3078d074799e2fbf5e847aeb2d71360/openhands-sdk/openhands/sdk/observability/laminar.py#L155-L180)),
and a search of the SDK repository finds no OTLP log or metric exporter
(checked 2026-10-19). No
span attribute in the 2026-10-19 capture carries workspace, repository, or
branch identity.

### Connector mapping

Not applicable — no native logs signal exists for the connector to consume.

## Metrics

### Native support

None exported. `llm.metrics` (`accumulated_cost`, `accumulated_token_usage`
with prompt/completion/cache_read/cache_write/reasoning tokens,
`context_window`, `costs`, `token_usages`, `response_latencies`) and
`conversation.conversation_stats` are in-process Python objects only — no
OTLP metrics endpoint exists to export them.

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (see
`connector/codingagentconnector/factory.go`); it does not process,
transform, or filter OTel metrics for any harness. Moot here regardless,
since OpenHands exports no OTel metrics to pass through.

## Sources

- OpenHands SDK observability: https://docs.openhands.dev/sdk/guides/observability (checked 2026-10-19)
- OpenHands SDK metrics: https://docs.openhands.dev/sdk/guides/metrics (checked 2026-10-19)
- OpenHands SDK v1.56.0 source (`33d8aa2`):
  https://github.com/OpenHands/software-agent-sdk/tree/33d8aa29c3078d074799e2fbf5e847aeb2d71360
  (checked 2026-10-19)
- Laminar Python SDK `lmnr` 0.7.67 LiteLLM instrumentation:
  https://github.com/lmnr-ai/lmnr-python/tree/0.7.67/src/lmnr/opentelemetry_lib/opentelemetry/instrumentation/litellm
  (checked 2026-10-19)
- Agent Canvas (`OpenHands/OpenHands` main, `8d39305`):
  https://github.com/OpenHands/OpenHands/tree/8d39305538c1d17a30cbe477e57a0e18fcee7d95
  (checked 2026-10-19)
- Live capture 2026-10-19: OpenHands SDK 1.56.0, `lmnr` 0.7.67, LiteLLM
  model `openrouter/z-ai/glm-5.3-flash`, via the repo's OpenHands e2e stack
- [docs/otel-signals.md](../otel-signals.md) (repo-internal signal matrix, refreshed 2026-08-29)
- [docs/metrics.md](../metrics.md) (repo-internal metrics catalog, refreshed 2026-08-21)
