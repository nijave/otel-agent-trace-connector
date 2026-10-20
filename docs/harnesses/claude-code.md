# Claude Code — OpenTelemetry signal reference

Claude Code exports three independent OpenTelemetry signals — traces, logs,
and metrics — each with its own enable switch and exporter. This file covers
all three, then details the raw → canonical attribute matrix for the one
signal the connector actually claims (traces). See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind the mapping.

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | native, beta — `claude_code.*` spans, scope `com.anthropic.claude_code.tracing`; enable with `OTEL_TRACES_EXPORTER=otlp` + `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` | consumed — this is the connector's active edge for Claude Code |
| Logs | native, long-standing — structured `claude_code.*` events; enable with `OTEL_LOGS_EXPORTER=otlp` | not consumed |
| Metrics | native — 4 instruments under meter scope `com.anthropic.claude_code`; enable with `OTEL_METRICS_EXPORTER=otlp` | not applicable |

Last verified: Traces 2026-10-19 (Claude Code 2.1.304), Logs 2026-08-29,
Metrics 2026-08-21 (see Sources).

## Traces

### Native support

Claude Code emits native `claude_code.*` spans (scope
`com.anthropic.claude_code.tracing`) when `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`
and `OTEL_TRACES_EXPORTER=otlp` reach its process — this signal is still
beta. Spans cover each interaction, model request, tool call, and hook.

### Connector mapping

The connector claims groups containing those spans, renames the three
top-level span types, remaps their vendor attributes onto the canonical
vocabulary, strips every attribute outside that vocabulary, and filters
resource attributes down to the canonical resource identity keys. Spans in a
claimed group whose names lack the `claude_code.` prefix (sibling
instrumentation scopes swept in by the group claim) drop from canonical
output; the raw pipelines preserve the originals.

Span-name renames:

| Raw span name | Canonical span name |
|---|---|
| `claude_code.interaction` | `invoke_agent claude_code` |
| `claude_code.llm_request` | `chat <model>` |
| `claude_code.tool` | `execute_tool <tool>` |
| other `claude_code.*` sub-spans | unchanged |

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

##### claude_code.interaction

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `session.id` | interaction | `gen_ai.conversation.id` | mapped (interaction root only, from the span or resource `session.id` when present; yields to a native `gen_ai.conversation.id`, and a batch without an interaction span emits no conversation id on any span) |
| `gen_ai.conversation.id` | interaction | `gen_ai.conversation.id` | kept (already canonical; Claude Code 2.1.304 emits it natively and it wins over the `session.id` fallback — absent when the span carries no native id and no session id) |
| `gen_ai.operation.name` | interaction | `gen_ai.operation.name` | kept (already canonical; the connector always writes `invoke_agent` from the span name) |
| `gen_ai.provider.name` | interaction | `gen_ai.provider.name` | kept (already canonical; the connector always writes `anthropic`) |
| `parent.source` | interaction | — | dropped |
| `queued_sends` | interaction | — | dropped |
| `user_prompt` | interaction | — | dropped |
| `user_prompt_length` | interaction | — | dropped |
| `interaction.sequence` | interaction | — | dropped |
| `interaction.duration_ms` | interaction | — | dropped |
| `user.id` | interaction | `coding_agent.user.id` | mapped (only when `capture_identity` is on) |
| `terminal.type` | interaction | `coding_agent.terminal.type` | mapped (unconditionally; the flag does not control it) |
| `span.type` | interaction | — | dropped |

##### claude_code.llm_request

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `input_tokens` | llm_request | `gen_ai.usage.input_tokens` | mapped (each counter only when the native span carries it; a usage-less llm_request still emits its chat span) |
| `output_tokens` | llm_request | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `cache_read_tokens` | llm_request | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition; the wire reports zero as `0`, not by omission) |
| `cache_creation_tokens` | llm_request | `gen_ai.usage.cache_write.input_tokens` | mapped (same condition) |
| `ttft_ms` | llm_request | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; absent when the native span carries no `ttft_ms`) |
| `first_content_ms` | llm_request | — | dropped (the canonical time to first chunk comes from `ttft_ms`; Claude Code 2.1.304 carries both with the same value) |
| `effort` | llm_request | `gen_ai.request.reasoning.level` | mapped (verbatim; absent when the native span carries no `effort`, as non-reasoning models do) |
| `stop_reason` | llm_request | `gen_ai.response.finish_reasons` | mapped (when present; appended only if the canonical key is absent) |
| `model` | llm_request | `gen_ai.request.model` | mapped (fallback when the canonical key is absent; with neither, the span keeps the bare `chat` name and no key) |
| `gen_ai.response.finish_reasons` | llm_request | `gen_ai.response.finish_reasons` | kept (already canonical) |
| `gen_ai.conversation.id` | llm_request | `gen_ai.conversation.id` | kept (already canonical; absent when the native span carries none — the edge never derives one for chat spans, so a batch without the interaction root keeps exactly what the wire sent) |
| `gen_ai.provider.name` | llm_request | `gen_ai.provider.name` | kept (already canonical; the connector always writes `anthropic`) |
| `query_source_safe` | llm_request | — | dropped |
| `duration_ms` | llm_request | — | dropped |
| `speed` | llm_request | — | dropped |
| `llm_request.context` | llm_request | — | dropped |
| `attempt` | llm_request | — | dropped |
| `success` | llm_request | — | dropped |
| `gen_ai.system` | llm_request | — | dropped (`gen_ai.provider.name` is connector-derived, not remapped) |
| `session.id`, `user.id`, `terminal.type`, `span.type` | llm_request | — | dropped |

##### claude_code.tool

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `tool_name` | tool | `gen_ai.tool.name` | mapped (fallback when the canonical key is absent) |
| `gen_ai.tool.name` | tool | `gen_ai.tool.name` | kept (already canonical; Claude Code 2.1.304 emits it and it wins over the `tool_name` fallback) |
| `gen_ai.operation.name` | tool | `gen_ai.operation.name` | kept (already canonical; the connector always writes `execute_tool` from the span name) |
| `gen_ai.conversation.id` | tool / tool.execution / tool.blocked_on_user | `gen_ai.conversation.id` | kept (already canonical; absent when the native span carries none) |
| `tool_name_safe` | tool | — | dropped |
| `bash_argv0` | tool | — | dropped |
| `bash_command_class` | tool | — | dropped |
| `gen_ai.tool.call.id` | tool / tool.execution | `gen_ai.tool.call.id` | kept (already canonical) |
| `tool_use_id` | tool / tool.execution | — | dropped |
| `duration_ms` | tool / tool.execution | — | dropped |
| `success` | tool.execution | — | dropped (failure still maps to span Status Error upstream of this connector's vocabulary where applicable) |
| `decision` | tool.blocked_on_user | — | dropped |
| `source` | tool.blocked_on_user | — | dropped |
| `session.id`, `user.id`, `terminal.type`, `span.type` | all tool spans | — | dropped |

Unrenamed sub-spans (`claude_code.tool.execution`,
`claude_code.tool.blocked_on_user`, hooks) receive the shared provenance keys
(`gen_ai.operation.name`, `gen_ai.provider.name`, `coding_agent.source`,
`coding_agent.client.name`, plus `coding_agent.client.version` when the
resource carries `service.version`) but keep nothing else outside the
vocabulary. Their `gen_ai.operation.name` derives from the span name with the
`claude_code.` prefix trimmed: names starting with `tool` collapse to
`execute_tool`; anything else keeps the trimmed name as a non-semconv
operation value (`claude_code.hook.pre_tool` → `hook.pre_tool`).

##### Native canonical keys and precedence

From 2.1.304 on Claude Code emits some canonical keys itself:
`gen_ai.conversation.id` on every span type, `gen_ai.operation.name` on
interaction and tool spans, `gen_ai.provider.name` on all of them, and
`gen_ai.tool.name` on tool spans. Each such key therefore has two potential
sources, and the edge resolves the overlap by kind rather than last-writer-wins
accident:

- **Wire data wins.** `gen_ai.conversation.id` and `gen_ai.tool.name` are
  facts the harness knows better than the connector: the native value is read
  first and the edge's own derivation (`session.id`, `tool_name`) is only a
  fallback for spans that omit it. `gen_ai.response.finish_reasons` follows the
  same rule — the native slice is kept and `stop_reason` is appended.
- **The connector's provenance wins.** `gen_ai.operation.name` and
  `gen_ai.provider.name` are rewritten from the span name and from the fact
  that a claimed group is Claude Code. A native copy is overwritten, so the two
  cannot disagree in canonical output.

Both rules are pinned by `TestClaudeNativeCanonicalKeysCannotConflict`, which
feeds deliberately conflicting native values through the edge.

##### Span events

| Raw event | Canonical fate | Status |
|---|---|---|
| `gen_ai.request.attempt` (attr `attempt`) | event survives, non-canonical attributes stripped | dropped |

#### Canonical keys with no Claude Code source

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
| `gen_ai.tool.type` / `gen_ai.tool.status` | not provided |
| `gen_ai.event.start_time` / `gen_ai.event.end_time` | not provided |
| `coding_agent.source.scope` | not provided (this edge writes `coding_agent.source.event` instead) |

#### Connector-written attributes

The connector writes these itself rather than remapping them from raw keys:

- `coding_agent.source` = `native`
- `coding_agent.client.name` = `claude_code`
- `coding_agent.client.version` = resource `service.version` (when present)
- `coding_agent.source.event` = original `claude_code.*` span name on the
  three renamed types
- `gen_ai.provider.name` = `anthropic`
- `gen_ai.operation.name`, `gen_ai.agent.name` (interaction root)

The last two are authoritative: a native copy of either key is overwritten, so
the connector's value and the harness's cannot disagree in canonical output
(see Native canonical keys and precedence above).

## Logs

### Native support

Independent of the traces beta, Claude Code has long exported OTel log
events via `OTEL_LOGS_EXPORTER=otlp` (needs only
`CLAUDE_CODE_ENABLE_TELEMETRY=1`, no beta flag). Structured events include
`claude_code.user_prompt`, `claude_code.tool_result`,
`claude_code.tool_decision`, `claude_code.api_request`,
`claude_code.api_error`, `claude_code.api_refusal`,
`claude_code.mcp_server_connection`, and `claude_code.permission_mode_changed`.
Content stays redacted by default; the operator opts in per field with
`OTEL_LOG_USER_PROMPTS` (prompt text), `OTEL_LOG_TOOL_DETAILS` (tool inputs
and parameters), `OTEL_LOG_TOOL_CONTENT` (tool result content), and
`OTEL_LOG_RAW_API_BODIES` (raw API request/response bodies) — all four
default to off.

### Connector mapping

Not consumed. The connector's Claude Code edge claims only spans on the
`claude_code.` traces scope (above); it does not claim or normalize Claude
Code's logs signal today.

## Metrics

### Native support

Claude Code emits four instruments under meter scope
`com.anthropic.claude_code`, enabled with `CLAUDE_CODE_ENABLE_TELEMETRY=1` and
`OTEL_METRICS_EXPORTER=otlp`:

| Metric | Type | Attributes |
| --- | --- | --- |
| `claude_code.session.count` | Sum (delta) | not enumerated from source |
| `claude_code.token.usage` | not enumerated from source | carries a `type` dimension |
| `claude_code.cost.usage` | not enumerated from source | not enumerated from source |
| `claude_code.lines_of_code.count` | not enumerated from source | not enumerated from source |

Full attribute sets beyond the `type` dimension were not captured from
source for this catalog; see `docs/metrics.md` for the sourcing caveat.

### Connector mapping

Not applicable. The connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (see
`connector/codingagentconnector/factory.go`); it does not process, transform,
or filter OTel metrics for any harness. Claude Code's native metrics, where
exported, pass through a metrics pipeline unmodified, outside this
connector's scope.

## Sources

- Claude Code monitoring docs: https://code.claude.com/docs/en/monitoring-usage (traces, logs, metrics; verified 2026-08-29)
- Claude Code Agent SDK observability docs: https://code.claude.com/docs/en/agent-sdk/observability (verified 2026-08-29)
- [docs/otel-signals.md](../otel-signals.md) — repo-internal signal-support research, refreshed 2026-08-29
- [docs/metrics.md](../metrics.md) — repo-internal metrics instrument catalog, refreshed 2026-08-21
- Traces attribute matrix above: derived from the pinned e2e wire capture of
  Claude Code 2.1.304, last updated 2026-10-19
