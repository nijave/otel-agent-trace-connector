# Codex — OpenTelemetry signal reference

Codex exports structured OTLP **logs** (`codex.*` events with
`conversation.id`). With `otel.trace_exporter` configured it also exports
native trace spans. The connector has an edge for each: the logs edge
deduplicates redelivered records, correlates each turn's events into a
deterministic trace, and emits one `invoke_agent codex` root with
`chat <model>` and `execute_tool <tool>` children plus the remaining raw
events attached to the root as span events (names only); the traces edge
keeps the turn, response, and tool spans of Codex's native trace and rebuilds
their attributes. See [canonical attributes](../canonical-attributes.md) for
the shared vocabulary and the policy behind it.

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | native, opt-in (`otel.trace_exporter`, default `none`) | traces edge (this file's Traces → Connector mapping; off by default upstream) |
| Logs | native (`codex.*` structured events) | logs edge (this file's Connector mapping) |
| Metrics | native (in-tree `codex-rs/otel` crate) | not applicable |

Last verified: 2026-10-19 for all three signals, against the Codex 0.163.0
capture and the `rust-v0.163.0` source tag (see Sources).

## Traces

### Native support

Native and opt-in. Codex 0.163.0 has a separate `otel.trace_exporter` key
(`none` by default; `otlp-http` or `otlp-grpc` turn it on). With an OTLP
trace exporter, Codex exports its internal `tracing` spans (for example the
`codex.exec` root span of `codex exec`) except `h2`/`tokio`/`runtime` spans.
It attaches trace-safe copies of some `codex.*` events (for example `codex.tool_result` and the
usage-bearing `response.completed`) as span events. `otel.span_attributes`
adds attributes to every exported span, and `codex exec` parents its root
span from a `TRACEPARENT` environment variable. Sources (rust-v0.163.0,
checked 2026-10-19): [`config/src/types.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/config/src/types.rs#L642-L650),
[`otel/src/provider.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/otel/src/provider.rs#L341-L350),
[`exec/src/lib.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/exec/src/lib.rs#L686-L697),
[`otel/README.md`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/otel/README.md).

The e2e config sets both `exporter` and `trace_exporter`, so the Codex stack
exercises both edges; the traces fixture is
`internal/codextraces/testdata/codex-native-traces.json` (captured
2026-10-23 from Codex 0.163.0, `codex exec`, API-key auth).

### Connector mapping

The connector claims a resource group whose instrumentation scope equals the
resource `service.name` (Codex's tracer takes the service name as its scope:
`codex_exec` for `codex exec`) and that holds a Codex marker: a
`session_task.*`, `codex.exec`, or `op.dispatch.*` span, a span with a
`codex.*` attribute key, or a span carrying a `codex.tool_result` event. The
attribute and event markers matter because Codex exports a span when it ends,
so the response and tool spans of a turn often arrive in a group, or a whole
export, that has no turn span yet. The edge keeps three span families,
rebuilds their attributes from scratch, and drops every other span
(tracing-opentelemetry bookkeeping included). IDs, kinds, and status pass
through; the trace id is Codex's own.

Parent linkage is best-effort because the edge is stateless. Within one
export, a kept span whose wire parent dropped reparents onto the earliest
turn root carrying its trace id, even when the turn sits in a different
resource group of that export. A kept span that exports before its turn keeps
its wire parent id (a dropped span) and its trace id, and a backend groups it
with the turn by trace id. The 2026-10-23 capture also shows one tool span
under a detached task with a trace id of its own; it stays parentless and
joins the turn only through `gen_ai.conversation.id` on the root.

Failure is not on the pinned capture's wire: no span carries Status Error and
none carries an error attribute, so the edge applies no failure rule of its
own and status simply passes through — a wire Error on a kept turn or response
span reaches canonical output unchanged. A call that fails before reporting
usage carries no `gen_ai.usage.input_tokens`, so its response span drops and
the failure stays invisible on this edge; the logs edge is the Codex source for
failures (see [Logs edge vs native traces edge](#logs-edge-vs-native-traces-edge)).

Running Codex logs and traces into one connector produces two canonical traces
per turn, one from each edge (`coding_agent.source` = `normalized` from logs,
`native` from traces). Pick one with the connector's `signals` setting or by
pipeline wiring; the connector does not dedupe across signals. The e2e stack
runs both on purpose and its validator requires both roots.

| Raw span | Canonical span name |
|---|---|
| `session_task.turn` (also `.compact`, `.review`) | `invoke_agent codex` (parent cleared) |
| the response-handling span carrying `gen_ai.usage.input_tokens` (`handle_responses` in the capture; Codex names it after the response event type, so the attribute is the discriminator) | `chat <model>` |
| `mcp.tools.call` | `execute_tool <tool.name>` |
| any span carrying a `codex.tool_result` event (`dispatch_tool_call_with_state` in the capture) | `execute_tool <tool_name>` |
| everything else (`run_turn`, `session_task.run`, `handle_responses` without usage, `codex.sampling`, `fs.*`, …) | dropped |

#### Attribute matrix

##### session_task.turn → invoke_agent

| Raw key | Canonical key | Status |
|---|---|---|
| `thread.id` | `gen_ai.conversation.id` | mapped (the span carries two `thread.id` entries, tracing's integer thread id and Codex's string thread UUID; the edge reads the string; absent when the span carries neither a string `thread.id` nor `conversation.id`) |
| `model` | `gen_ai.request.model` | mapped (also supplies the chat spans' model; absent when the turn span carries none) |
| `codex.turn.reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (absent when no effort applies) |
| `codex.turn.token_usage.input_tokens` | `gen_ai.usage.input_tokens` | mapped (each counter only when the span carries it; Codex records them when the task finishes, so a turn exported before then has none) |
| `codex.turn.token_usage.cached_input_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition) |
| `codex.turn.token_usage.cache_write_input_tokens` | `gen_ai.usage.cache_write.input_tokens` | mapped (same condition) |
| `codex.turn.token_usage.output_tokens` | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `codex.turn.token_usage.reasoning_output_tokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (same condition) |
| `codex.turn.token_usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped (same condition) |
| `codex.turn.token_usage.non_cached_input_tokens`, `turn.id` | — | dropped |
| `code.*`, `thread.name`, `busy_ns`, `idle_ns`, `target`, `level` | — | dropped (tracing-opentelemetry bookkeeping on every span) |

##### response span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `gen_ai.usage.input_tokens`, `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_write.input_tokens`, `gen_ai.usage.output_tokens` | same | kept (recorded when the stream completes; the input-token key is also the span's discriminator) |
| `codex.usage.reasoning_output_tokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (absent when the provider reports none) |
| `codex.usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped (same) |
| `codex.request.reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (absent when no effort applies) |
| (turn span's `model`) | `gen_ai.request.model` | mapped from the earliest turn span carrying the same trace ID in this export (absent, and the span named bare `chat`, when that trace has no model-bearing turn span in the export; turns in other traces never supply the model) |
| `from`, `tool_name` | — | dropped |

##### mcp.tools.call → execute_tool

Shape taken from the `rust-v0.163.0` source; the e2e stack runs no MCP
server, so the capture carries none of these spans.

| Raw key | Canonical key | Status |
|---|---|---|
| `tool.name` | `gen_ai.tool.name` | mapped (also names the span; absent when the span carries none) |
| `tool.call_id` | `gen_ai.tool.call.id` | mapped (absent when empty) |
| `server.address`, `server.port` | same | kept (absent for stdio servers) |
| record kind | `gen_ai.tool.type` = `mcp` | mapped |
| `mcp.server.name`, `mcp.server.origin`, `mcp.transport`, `mcp.connector.*`, `codex.mcp.*`, `rpc.*`, `conversation.id`, `session.id`, `turn.id`, `error.type` | — | dropped (span Status passes through) |

##### codex.tool_result event → execute_tool

| Raw key (event attribute) | Canonical key | Status |
|---|---|---|
| `tool_name` | `gen_ai.tool.name` | mapped (also names the span; absent when the event carries none) |
| `call_id` | `gen_ai.tool.call.id` | mapped (absent when empty) |
| record kind | `gen_ai.tool.type` = `builtin` | mapped |
| `success` = false (bool or string) | span Status = Error, message `tool execution failed` | mapped to status |
| `duration_ms`, `tool_namespace`, `tool_result_seq`, `output_truncated`, `arguments_length`, `output_length`, `output_line_count`, `tool_origin`, `mcp_tool`, `product_sku`, `slug`, `originator`, `terminal.type`, `model`, `conversation.id`, `app.version` | — | dropped (the whole event drops) |

#### Span events

Kept spans use the shared canonical event policy. Known GenAI content events
are removed whole. Other operational events keep their names and timestamps,
but their attributes are reduced to the canonical allowlist. The
`codex.tool_result` event used to construct a builtin tool span is removed
whole after its tool identity and outcome are mapped. Raw events remain
unchanged in the raw pipeline.

#### Connector-written attributes

- `coding_agent.source` = `native`
- `coding_agent.client.name` = `codex`
- `coding_agent.client.version` = resource `service.version` (when present)
- `coding_agent.source.event` = the wire span name
- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `codex` (root only)
- `gen_ai.provider.name` = `openai` (root and chat spans)

#### Canonical keys with no source on the traces wire

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (the usage-bearing `response.completed` log event, which carries `ttft_ms`, never reaches traces; use the logs edge for TTFT) |
| `coding_agent.request.service_tier` | not provided (same reason) |
| `coding_agent.user.id` / `coding_agent.user.email` / `coding_agent.terminal.type` | not provided (trace-safe events omit identity and the spans carry none) |
| `gen_ai.response.finish_reasons` / `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `gen_ai.agent.id` / `coding_agent.agent.parent_id` | not provided (the trace-safe `codex.tool_result` event omits `agent_name`; use the logs edge for subagent attribution) |
| `coding_agent.usage.cost` | not provided (`codex.turn_cost` is log-only) |
| `coding_agent.request.attempt` | not provided (the traces wire carries no attempt count; the logs wire's `codex.api_request` event carries a 0-based `attempt` retry counter, but that event produces no canonical span and the connector records no 0-based number under this 1-based key) |

## Logs

### Native support

Native structured OTLP logs: `codex.*` events carrying `conversation.id`.
Configure via the `[otel]` block in the user-level `~/.codex/config.toml`
(Codex ignores project-local `[otel]` tables):

```toml
[otel]
log_user_prompt = false
exporter = { otlp-http = { endpoint = "http://collector:4318/v1/logs", protocol = "binary" } }
```

### Connector mapping

Event-to-span mapping:

| Raw log event | Canonical output |
|---|---|
| `codex.sse_event` (`event.kind=response.completed`, usage/ttft-bearing) | `chat <model>` span |
| `codex.sse_event` (`event.kind=response.completed`, carrying only `duration_ms`) | duplicate of the same call's usage-bearing record → dropped; the call's only record when the provider reported no usage → `chat <model>` span without usage keys |
| failed `response.completed` carrying only `error.message` | `chat <model>` span with Status Error and the message (Codex logs one such record per failed response stream) |
| `codex.user_prompt` | turn boundary / root source event (content stripped) |
| `codex.tool_result` | `execute_tool <tool>` span |
| `codex.api_request`, `codex.startup_phase`, … | root span events (name + timestamp only) |
| `codex.tool_decision` | dropped entirely |
| everything else | root span events (name + timestamp only) |

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

Usage depends on the provider. Codex 0.163.0 logs the usage-bearing
`response.completed` record only when the provider's `response.completed`
carries a `usage` object. That record always has all six token counts, with 0
for a detail field (cached, cache-write, reasoning) that the provider omits.
When the provider sends no `usage`, Codex logs only the SSE frame record for
that call — the record the frame poll writes with Codex-measured `duration_ms`
— so the connector keeps it as the call's `chat <model>` span, without the keys
the usage-bearing record alone carries: all six token counts, `ttft_ms`,
`model_reasoning_effort`, and `service_tier`. Codex emits the frame record
before the usage-bearing one for the same call, and the connector sees the
whole turn before building spans, so a frame record is the duplicate of exactly
the next `response.completed` record in the turn when that record reports the
call, and a model call in its own right otherwise. Codex 0.163.0
speaks only the Responses wire API. Sources (rust-v0.163.0, checked
2026-10-19): [`core/src/client.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/core/src/client.rs#L2579-L2580),
[`otel/src/events/session_telemetry.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/otel/src/events/session_telemetry.rs#L1057-L1146),
[`codex-api/src/sse/responses.rs` poll site](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/codex-api/src/sse/responses.rs#L553-L561),
[`codex-api/src/sse/responses.rs` wire types](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/codex-api/src/sse/responses.rs#L119-L145),
[`model-provider-info/src/lib.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/model-provider-info/src/lib.rs#L107-L111).

Wire drift (audited 2026-10-19 against the `rust-v0.163.0` tag): the pinned
e2e capture now comes from Codex 0.163.0. That version sends
`cache_write_token_count` on usage-bearing `response.completed` records
(mapped below) and extra detail fields on `codex.tool_result` and
`codex.tool_decision`; those detail fields appear as dropped rows in the
matrix below. The builder copies only the keys in this matrix, so they stay
out of canonical output.

##### codex.sse_event (response.completed) → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `input_token_count` | `gen_ai.usage.input_tokens` | mapped (absent when the provider reported no usage: then only the frame record exists) |
| `output_token_count` | `gen_ai.usage.output_tokens` | mapped (absent when the provider reported no usage: then only the frame record exists) |
| `cached_token_count` | `gen_ai.usage.cache_read.input_tokens` | mapped (0, not absent, when the provider's usage has no `input_tokens_details`; absent when the provider reported no usage, which leaves only the frame record) |
| `cache_write_token_count` | `gen_ai.usage.cache_write.input_tokens` | mapped (0, not absent, when the provider's usage has no cache-write field; the pinned 0.163.0 capture reports it non-zero — 7924 then 105 across the two completions; absent when the provider reported no usage) |
| `tool_token_count` | `gen_ai.usage.total_tokens` | mapped (Codex logs the provider's `usage.total_tokens` under this name; the connector never computes a total; absent when the provider reported no usage) |
| `reasoning_token_count` | `gen_ai.usage.reasoning.output_tokens` | mapped (0, not absent, when the provider's usage has no `output_tokens_details`; replaces the former vendor `coding_agent.usage.reasoning_tokens`; absent when the provider reported no usage) |
| `ttft_ms` | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; absent when Codex saw no `response.output_item.added` before the completion, and absent when the provider reported no usage, because the frame record that then stands in for the call carries no `ttft_ms`; either `ttft_ms` or a token count identifies a record as the call's usage-bearing one) |
| `model` | `gen_ai.request.model` | mapped (also names the span; the frame record carries it too) |
| `model_reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (the request's effort, not a provider value; absent when neither config nor the model's default sets one — Codex sends an effort of `none` as the string `none`; the pinned 0.163.0 capture carries `low`; absent when the provider reported no usage, because the frame record carries no effort field) |
| `service_tier` | `coding_agent.request.service_tier` | mapped (the request's tier, not a provider value; absent when the request sets no service tier, as in the pinned 0.163.0 capture; absent when the provider reported no usage, because the frame record carries no tier field) |
| `duration_ms` | — | dropped (used for span bounds only; the only key that identifies the frame record) |
| `event.kind`, `event.timestamp` | — | dropped (span name/bounds carry them) |
| `error.message` | span Status = Error, message = the wire value | mapped to status (a record carrying it is the failed call's chat span; no attribute is written, and the record is never treated as a frame record) |
| `slug`, `originator`, `terminal.type`, `auth_mode` | — | dropped |

##### codex.tool_result → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `tool_name` | `gen_ai.tool.name` | mapped (also names the span; a record without `tool_name` gets the literal `unknown`) |
| `success` = false | span Status = Error, message `tool execution failed` | mapped to status (the connector drops the boolean attribute itself) |
| `call_id` | `gen_ai.tool.call.id` | mapped (absent when the record carries none) |
| `duration_ms` | — | dropped (used for span bounds only) |
| `arguments`, `output` | — | dropped (tool content, stripped at parse time) |
| `mcp_server`, `mcp_server_origin` | — | dropped |
| `tool_namespace` | — | dropped (tool provenance detail, outside the usage/cost/performance scope) |
| `tool_result_seq` | — | dropped (process-wide result counter, not a per-turn ordinal) |
| `output_truncated` | — | dropped (qualifies tool output content that the connector strips at parse time) |
| `agent_name` | `gen_ai.agent.id` | mapped (verbatim on execute_tool spans; `/root` for the main agent, as in the pinned capture, otherwise the subagent's path, nickname, or conversation UUID; absent on wires older than 0.163.0 and on a record without it; the root keeps `gen_ai.agent.name` = `codex` and no agent id) |
| `product_sku` | — | dropped (absent from the pinned capture) |

##### invoke_agent codex root

| Raw key | Canonical key | Status |
|---|---|---|
| `conversation.id` | `gen_ai.conversation.id` | mapped |
| `model` | `gen_ai.request.model` | mapped (last observed value in the turn) |
| `app.version` | `coding_agent.client.version` | mapped |
| `user.account_id` | `coding_agent.user.id` | mapped (only when `capture_identity` is on; absent when the run authenticates with an API key, because only ChatGPT-style auth carries it. Upstream describes it as the workspace account ID, not a per-user ID) |
| `user.email` | `coding_agent.user.email` | mapped (only when `capture_identity` is on; absent under API-key auth for the same reason) |
| `terminal.type` | `coding_agent.terminal.type` | mapped (unconditionally; the flag does not control it) |
| `provider_name` | — | dropped (operator-authored display label from config.toml, not a known provider identifier; `gen_ai.provider.name` stays `openai`, the wire protocol). Only the session's first turn logs it anyway |
| `coding_agent.turn.finish_reason` | — | dropped (was **connector-derived** — `completed`/`superseded`/`timeout`/`evicted`/`shutdown` finalization reasons, not a model stop reason; still exposed as the `otelcol_coding_agent_turns_emitted` metric's `finish_reason` label. Timeouts surface as root span Status Error) |
| `coding_agent.turn.complete` | — | dropped (derivable from the metric label above) |
| `coding_agent.turn.prompt_observed` | — | dropped |
| `coding_agent.turn.events_truncated` | — | dropped (still exposed via `otelcol_coding_agent_turns_truncated`) |
| turn-total usage (summed token counts on the root) | — | dropped (usage lives on chat spans only; sum them for turn totals) |
| root-event `error.message` copy | — | dropped (root events keep their names only) |
| root-event `event.kind` copy | — | dropped |

The turn resource's `host.name` survives `FilterResource` as a resource
identity key only when `capture_identity` is on; with the flag off, the
connector strips it along with the other identity keys above.

Identity is auth-dependent: an API-key run (the pinned e2e capture) logs no
`user.account_id`/`user.email` at all, so `coding_agent.user.id` and
`coding_agent.user.email` are simply absent. The committed fixture synthesizes
both keys (plus a synthetic `host.name`) so the `capture_identity` mappings
stay covered by the fixture-replay test.

##### codex.tool_decision

Dropped entirely, including the decision→result pairing: its only canonical
outputs were `coding_agent.tool.decision` / `coding_agent.tool.decision_source`
(both vendor keys outside the vocabulary), and once those went the join had no
remaining purpose. Codex 0.163.0 also sends `tool_namespace` on this event; it
drops with the event.

#### Connector-written attributes

The connector writes these itself rather than remapping them from raw keys;
they appear on every emitted span except where noted:

- `coding_agent.source` = `normalized`
- `coding_agent.client.name` = `codex`
- `coding_agent.source.event` = originating `codex.*` event name
- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.provider.name` = `openai` (the wire protocol; invoke_agent root and chat spans only — execute_tool spans never carry it)
- `gen_ai.agent.name` = `codex` (invoke_agent root only)

#### Canonical keys with no Codex source

| Canonical key | Status |
|---|---|
| `gen_ai.response.finish_reasons` | not provided (Codex logs no model stop reason; see the dropped connector-derived `finish_reason` above) |
| `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `gen_ai.agent.version` | not provided |
| `coding_agent.agent.parent_id` | not provided (Codex names the calling agent but not its parent) |
| `gen_ai.tool.type` / `gen_ai.tool.status` | not provided |
| `server.address` / `server.port` | not provided (proxied setups are indistinguishable) |
| `exception.*` | not provided |
| `coding_agent.usage.cost` | not provided (`codex.turn_cost` is a log-only event the edge does not read, and the pinned capture carries none) |
| `coding_agent.request.attempt` | not provided (the `codex.api_request` event carries a 0-based `attempt` retry counter, but that event produces no canonical span — it only continues the turn — and the connector records no 0-based number under this 1-based key; mapping it would also need turn state) |

## Logs edge vs native traces edge

Both edges carry the same Codex turns, and `signals` (see the README) picks
which one an operator consumes; the connector never dedupes across them. Over
the same 7 days (to 2026-11-08) the two implementations produced different
numbers for the same conversations: the logs edge 27 turns, 94 chat spans, 82 of
them carrying `gen_ai.response.time_to_first_chunk`, and 6.35M input tokens;
the native edge 17 turns, 13 chat spans, no TTFC, 11.75M input tokens, and 20 of
its 37 canonical traces without an `invoke_agent codex` root. The pinned
captures (`internal/codex/testdata/codex-native-logs.json`,
`internal/codextraces/testdata/codex-native-traces.json`) each hold one turn
with two model calls, and on them the edges agree: two chat spans each.

**Chat spans — one per call on both edges, not one per turn.** The logs edge
emits a chat span per `response.completed` record that describes a call
(`internal/codex/trace.go`, `appendChatSpans` / `describesModelCall`),
including the usage-less frame record that stands in for a call whose provider
reported no usage. The native edge classifies by the
`gen_ai.usage.input_tokens` attribute (`internal/codextraces/normalizer.go`,
`normalizeSpan` / `chatMarkerAttr`), and Codex records that attribute only on
the loop iteration that receives `ResponseEvent::Completed { token_usage:
Some(_) }` (`otel/src/events/session_telemetry.rs:653-672` in
`rust-v0.163.0`). So every native response span without usage — a provider that
reported none, a call that errored, an interrupted stream — drops, while the
logs edge still gives that call a chat span. Codex also opens
`handle_responses` inside its response-event loop
(`core/src/session/turn.rs:2715`), so one turn can export several response
spans for one call, and only the iteration that receives the completion
carries usage: the pinned traces capture holds four response spans for the
turn's two calls, and the two without usage drop. Those mechanisms cost the
native edge usage-less and failed calls, but both captures show per-call
parity, so they do not explain a 13-vs-94 shortfall; what remains points at
spans that never reached the collector (the native exporter is opt-in, and a
span exports only when it ends) — see the follow-ups.

**Failures.** The logs edge now sets Status Error and the wire's `error.message`
on the failed call's chat span; Codex logs exactly one such record per failed
response stream (`otel/src/events/session_telemetry.rs:1115-1126`). The native
edge keeps a wire Error status on a kept span but has no failure signal of its
own to map, and a failed call records no usage on its response span, so it
produces no chat span at all. Failures are therefore a logs-edge fact.

**TTFC — not on the native wire.** Codex records `ttft_ms` on the usage-bearing
`codex.sse_event` log record only; no trace span declares the field
(`core/src/session/turn.rs:2715-2726`), and no span in the pinned capture
carries it. The native edge maps no `gen_ai.response.time_to_first_chunk` (see
"Canonical keys with no source on the traces wire" above); use the logs edge for
TTFT.

**Input tokens — the same counters, counted twice.** Both edges copy the
provider's `usage.input_tokens` verbatim (the two captures agree on the first
call: 7927), but the native edge also maps the turn span's per-turn totals onto
the root (`internal/codextraces/normalizer.go`, `turnUsageKeys`), and a turn
total is exactly the sum of that turn's call counters: the pinned capture's root
carries 15965 for chats of 7927 and 8038, so summing
`gen_ai.usage.input_tokens` over every canonical span counts each token twice.
The logs edge drops turn totals for that reason (row in "invoke_agent codex
root" above), so the two edges are not sum-compatible. Cached tokens are not a
second count on either wire: `input_tokens` already includes them (the capture's
second chat reports 8038 input with 7924 cache-read and 111 cache-write, and the
turn's totals are the sums of its calls' counters: 7924 cache-read = 0 + 7924,
8035 cache-write = 7924 + 111). The 11.75M figure is consistent with a sum over
roots plus chats, while the logs edge carries no root usage at all. Mapping the
turn totals is deliberate — the two signals carry different facts (see
`docs/superpowers/specs/2026-10-19-harness-feature-gaps-design.md`) — so it
stays; a consumer comparing the edges must sum chat spans only.

**Rootless traces — spans whose turn is not in their batch.** The edge is
stateless: it reparents a kept child only onto a root with the same trace id in
the same batch (`internal/codextraces/normalizer.go`, `reparentOrphans`) and
keeps the wire parent id otherwise. Codex exports a span when it ends and closes
the turn span last, so a batch can carry a turn's response and tool spans
without the turn; and a span created in a detached task carries a trace id of
its own. The pinned capture shows both shapes: its canonical output has one
trace with the root (2 chats + 1 tool) and one rootless trace holding a single
`execute_tool exec_command` whose wire parent is not in the batch and whose
trace id differs from the turn's. Only a `session_task.turn` / `.compact` /
`.review` span can become a root (`turnSpanNames`); `codex.exec`,
`session_task.run`, and every other span drop, so none of them can supply a
missing one. Live, 20 of 37 native canonical traces have no root — the same
shape, at scale.

Follow-ups (each larger than a stateless mapping fix):

- **Find the missing native spans.** For one window, count `handle_responses`
  spans carrying `gen_ai.usage.input_tokens` in a raw traces pipeline and
  compare it with the canonical chat-span count. If the raw spans are already
  absent, the loss is upstream of the connector (opt-in `trace_exporter`
  coverage, or the exporter's bounded queue), not in this edge.
- **Rootless traces need correlation the edge cannot do statelessly:** a backend
  join on `gen_ai.conversation.id` (roots carry it; rootless children do not),
  or cross-batch turn correlation that would make the edge stateful.
- **Sum compatibility** between the edges would need the native root to stop
  mapping turn totals, which is the only usage the native edge keeps for a turn
  whose chat spans are missing. That is a product decision, not a bug fix.

## Metrics

### Native support

Native, in-tree `codex-rs/otel` crate — counters and histograms, verified
from source at `rust-v0.163.0` (commit `83086188`, checked 2026-10-19). The
default `otel.metrics_exporter` is `statsig`, which sends to OpenAI and
follows `[analytics] enabled`. To send metrics to your own collector, set
`otel.metrics_exporter` to an `otlp-http` or `otlp-grpc` exporter in
`~/.codex/config.toml`. Since Codex 0.163.0 (openai/codex#52278), a custom
OTLP metrics exporter works even with analytics turned off; set
`otel.metrics_exporter = "none"` to turn it off. The published
advanced-config docs still say disabling analytics also disables custom OTLP
metrics exporters; the source is authoritative. Sources:
[`core/src/otel_init.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/core/src/otel_init.rs#L68-L78),
[`config/src/types.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/config/src/types.rs#L645-L647),
[0.163.0 release notes](https://github.com/openai/codex/releases/tag/rust-v0.163.0).

Key instruments (full catalog, including every histogram and its complete
attribute set, lives in [docs/metrics.md](../metrics.md#codex)):

- Counters: `codex.thread.started`, `codex.conversation.turn.count`,
  `codex.tool.call`, `codex.tool.unified_exec`, `codex.websocket.request`,
  `codex.plugins.startup_sync`
- Histograms: `codex.turn.e2e_duration_ms`, `codex.turn.ttft.duration_ms`,
  `codex.turn.ttfm.duration_ms`, `codex.turn.token_usage` (by `token_type`),
  `codex.turn.tool.call`, `codex.mcp.tools.*.duration_ms`

Session tags (`auth_mode`, `session_source`, `originator`, `model`,
`app.version`) append to most session metrics. Global-client metrics
(`codex.mcp.tools.*`, `codex.remote_models.*`, `codex.plugins.startup_sync`)
carry none of those tags.

`docs/metrics.md` flags a doc-vs-source disagreement worth restating here:
the Last9 Codex doc's attribute keys are stale against this source
revision — it says `type` where source uses `token_type`, `mode` where
source uses `active`/`tmp_mem_enabled`, and `tool.name` where source uses
`tool`. Source keys are authoritative.

### Connector mapping

Not applicable — this connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (see
`connector/codingagentconnector/factory.go`); it does not process, transform,
or filter OTel metrics for any harness. Codex's native metrics, where
exported, pass through a metrics pipeline unmodified.

## Sources

- Codex source at tag `rust-v0.163.0` (commit `83086188`), checked 2026-10-19:
  https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs (`otel/README.md`, `otel/src/provider.rs`,
  `otel/src/events/session_telemetry.rs`, `otel/src/tool_result.rs`,
  `codex-api/src/sse/responses.rs`, `config/src/types.rs`, `config/src/loader/mod.rs`, `core/src/otel_init.rs`,
  `core/src/client.rs`)
- Codex 0.163.0 release notes: https://github.com/openai/codex/releases/tag/rust-v0.163.0 (checked 2026-10-19)
- Codex advanced configuration docs: https://developers.openai.com/codex/config-advanced (project-local `otel` ignored; checked 2026-10-19)
- [docs/otel-signals.md](../otel-signals.md) — top-level per-signal matrix (refreshed 2026-08-29; its Codex traces row predates the native traces finding above)
- [docs/harnesses.md](../harnesses.md) — summary table (refreshed 2026-08-20; its Codex row predates the native traces finding above)
- [docs/metrics.md](../metrics.md) — full Codex instrument catalog (refreshed 2026-08-21, verified against upstream clone commit `4f39251`)
- Wire capture behind the Logs attribute matrix above: Codex 0.163.0 (`codex exec`, API-key auth, Responses API), captured and audited 2026-10-19 against the `rust-v0.163.0` tag
- Wire capture behind the Traces attribute matrix above: Codex 0.163.0 (`codex exec`, API-key auth, `otel.trace_exporter` = otlp-http), captured 2026-10-23 from the e2e stack and sliced into `internal/codextraces/testdata/codex-native-traces.json`
