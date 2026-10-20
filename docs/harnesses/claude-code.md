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
| Traces | native, beta — `claude_code.*` spans, scope `com.anthropic.claude_code.tracing`; enable with `CLAUDE_CODE_ENABLE_TELEMETRY=1` + `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` + `OTEL_TRACES_EXPORTER=otlp` | consumed — this is the connector's active edge for Claude Code |
| Logs | native, long-standing — structured `claude_code.*` events; enable with `CLAUDE_CODE_ENABLE_TELEMETRY=1` + `OTEL_LOGS_EXPORTER=otlp` | not consumed |
| Metrics | native — 8 counters under meter `com.anthropic.claude_code`; enable with `CLAUDE_CODE_ENABLE_TELEMETRY=1` + `OTEL_METRICS_EXPORTER=otlp` | not applicable |

Last verified: 2026-10-19 for all three signals (Claude Code 2.1.304; see
Sources).

## Traces

### Native support

Claude Code emits native `claude_code.*` spans (scope
`com.anthropic.claude_code.tracing`) when `CLAUDE_CODE_ENABLE_TELEMETRY=1`,
`CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`, and `OTEL_TRACES_EXPORTER=otlp`
reach its process — this signal is still beta. Spans cover each interaction,
model request, and tool call; tool spans have `tool.blocked_on_user` and
`tool.execution` children. The `claude_code.hook` span appears only under
detailed beta tracing (`ENABLE_BETA_TRACING_DETAILED=1` plus
`BETA_TRACING_ENDPOINT`), not with the enhanced-telemetry flag alone
([monitoring docs, Traces (beta)](https://code.claude.com/docs/en/monitoring-usage#traces-beta),
checked 2026-10-19).

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
| `session.id` | interaction | `gen_ai.conversation.id` | mapped (interaction root only, from the span or resource `session.id` when present; yields to a native `gen_ai.conversation.id`; the edge derives no id for other span types, so in a batch without an interaction span only natively carried ids survive) |
| `gen_ai.conversation.id` | interaction | `gen_ai.conversation.id` | kept (already canonical; Claude Code 2.1.304 emits it natively and it wins over the `session.id` fallback — absent when the span carries no native id and no session id) |
| `gen_ai.operation.name` | interaction | `gen_ai.operation.name` | kept (already canonical; the connector always writes `invoke_agent` from the span name) |
| `gen_ai.provider.name` | interaction | `gen_ai.provider.name` | kept (already canonical; the connector always writes `anthropic`) |
| `parent.source` | interaction | — | dropped |
| `queued_sends` | interaction | — | dropped |
| `user_prompt` | interaction | — | dropped |
| `user_prompt_length` | interaction | — | dropped |
| `interaction.sequence` | interaction | — | dropped |
| `interaction.duration_ms` | interaction | — | dropped |
| `user.id` | interaction | `coding_agent.user.id` | mapped (only when `capture_identity` is on; absent when the span carries no `user.id`) |
| `user.email` | all span types | `coding_agent.user.email` | mapped (only when `capture_identity` is on; absent when the span carries no `user.email`, including the whole 2.1.304 capture; unlike `user.id` it is not root-only — every span type that carries it keeps it. The edge reads the span attribute only: a resource-carried copy would drop with the rest of the non-canonical resource keys) |
| `agent_id` | all span types | `gen_ai.agent.id` | mapped (verbatim; absent when the span carries no `agent_id` — the main agent's spans omit it and Claude Code sets it only under a subagent — and absent in the 2.1.304 capture; `capture_identity` does not control it) |
| `terminal.type` | interaction | `coding_agent.terminal.type` | mapped (absent when the span carries no `terminal.type`, which Claude Code sets only when it detects a terminal; `capture_identity` does not control it) |
| `span.type` | interaction | — | dropped |
| `organization.id`, `user.account_uuid`, `user.account_id` | all span types | — | dropped (standard attributes, documented upstream; absent in the 2.1.304 capture) |

##### claude_code.llm_request

| Raw key | Span type | Canonical key | Status |
|---|---|---|---|
| `input_tokens` | llm_request | `gen_ai.usage.input_tokens` | mapped (each counter only when the native span carries it; a usage-less llm_request still emits its chat span) |
| `output_tokens` | llm_request | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `cache_read_tokens` | llm_request | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition; the wire reports zero as `0`, not by omission) |
| `cache_creation_tokens` | llm_request | `gen_ai.usage.cache_write.input_tokens` | mapped (same condition) |
| `ttft_ms` | llm_request | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; absent when the native span carries no `ttft_ms`) |
| `first_content_ms` | llm_request | — | dropped (the canonical time to first chunk comes from `ttft_ms`; Claude Code 2.1.304 carries both with the same value) |
| `effort` | llm_request | `gen_ai.request.reasoning.level` | mapped (verbatim; absent when the native span carries no `effort`: Claude Code omits it when it sends no effort level, for example on a model without effort support, and before 2.1.274) |
| `stop_reason` | llm_request | `gen_ai.response.finish_reasons` | mapped (absent when the span carries no string `stop_reason`; appended to the canonical slice unless the slice already holds the same value) |
| `model` | llm_request | `gen_ai.request.model` | mapped (fallback when the canonical key is absent; with neither, the span keeps the bare `chat` name and no key) |
| `gen_ai.request.model` | llm_request | `gen_ai.request.model` | kept (already canonical; Claude Code emits it with the same value as `model`, and it wins over the `model` fallback) |
| `gen_ai.response.finish_reasons` | llm_request | `gen_ai.response.finish_reasons` | kept (already canonical) |
| `gen_ai.response.id` | llm_request | `gen_ai.response.id` | kept (already canonical; documented upstream as the API request id, absent in the 2.1.304 capture) |
| `gen_ai.conversation.id` | llm_request | `gen_ai.conversation.id` | kept (already canonical; absent when the native span carries none — the edge never derives one for chat spans, so a batch without the interaction root keeps exactly what the wire sent) |
| `gen_ai.provider.name` | llm_request | `gen_ai.provider.name` | kept (already canonical; the connector always writes `anthropic`) |
| `query_source_safe` | llm_request | — | dropped |
| `duration_ms` | llm_request | — | dropped |
| `speed` | llm_request | — | dropped |
| `llm_request.context` | llm_request | — | dropped |
| `attempt` | llm_request | — | dropped |
| `success` | llm_request | — | dropped |
| `query_source`, `parent_agent_id`, `workflow.run_id`, `workflow.name` | llm_request | — | dropped (documented upstream; absent in the 2.1.304 capture) |
| `request_id`, `client_request_id`, `status_code`, `error`, `error_class`, `response.has_tool_call` | llm_request | — | dropped (documented upstream; absent in the 2.1.304 capture) |
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
| `result_tokens`, `parent_agent_id`, `workflow.run_id`, `workflow.name` | tool | — | dropped (documented upstream; absent in the 2.1.304 capture) |
| `file_path`, `full_command`, `skill_name`, `subagent_type` | tool | — | dropped (content; Claude Code emits them only with `OTEL_LOG_TOOL_DETAILS=1`) |
| `success` | tool.execution | — | dropped (Claude Code also sets span status `ERROR` on a failed execution; the connector passes span status through unchanged) |
| `error`, `error_class` | tool.execution | — | dropped (present only on a failed execution) |
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
operation value (`claude_code.hook` → `hook`). The hook span's own attributes
(`hook_event`, `hook_name`, `num_*`, `duration_ms`, `hook_definitions`) all
drop.

Detailed beta tracing also adds content attributes (`new_context`,
`system_reminders`, `system_prompt_preview`, `user_system_prompt`,
`response.model_output`, `tool_input`). None is in the canonical vocabulary,
so all drop.

##### Native canonical keys and precedence

The 2.1.304 capture carries some canonical keys natively:
`gen_ai.conversation.id` and `gen_ai.provider.name` on every span type,
`gen_ai.operation.name` on interaction and tool spans,
`gen_ai.request.model` and `gen_ai.response.finish_reasons` on llm_request
spans, `gen_ai.tool.name` on tool spans, and `gen_ai.tool.call.id` on tool and
tool.execution spans. A key with both a native value and an edge derivation
has two potential sources, and the edge resolves the overlap by kind rather
than last-writer-wins accident:

- **Wire data wins.** `gen_ai.conversation.id`, `gen_ai.request.model`, and
  `gen_ai.tool.name` are facts the harness knows better than the connector:
  the edge reads the native value first and uses its own derivation
  (`session.id`, `model`, `tool_name`) only as a fallback for spans that omit
  it. `gen_ai.response.finish_reasons` follows the same rule — the edge keeps
  the native slice and appends `stop_reason` only when the slice lacks it.
- **The connector's provenance wins.** The edge rewrites
  `gen_ai.operation.name` and `gen_ai.provider.name` from the span name and
  from the fact that a claimed group is Claude Code. It overwrites a native
  copy, so the two cannot disagree in canonical output.

`TestClaudeNativeCanonicalKeysCannotConflict` pins both rules: it feeds
deliberately conflicting native values through the edge.

##### Span events

Only names on the content-event denylist in `internal/content` lose the whole
event; every other event survives with its attributes reduced to the
canonical allowlist.

| Raw event | Canonical fate | Status |
|---|---|---|
| `gen_ai.request.attempt` on llm_request (attrs `attempt`, `client_request_id`) | event survives, both attributes stripped | dropped |
| `tool.output` on tool (attrs `content`, `output`, `diff`, `file_path`, `bash_command`; emitted only with `OTEL_LOG_TOOL_CONTENT=1`) | event survives, every attribute stripped | dropped |

#### Canonical keys with no Claude Code source

| Canonical key | Status |
|---|---|
| `gen_ai.usage.total_tokens` | not provided (no raw total-token source) |
| `gen_ai.usage.reasoning.output_tokens` | not provided (no reasoning-token source) |
| `gen_ai.response.model` | not provided |
| `gen_ai.request.max_tokens` | not provided |
| `gen_ai.request.stream` | not provided |
| `gen_ai.agent.version` | not provided |
| `coding_agent.team.id` | not provided (the `organization.id` standard attribute drops; the edge does not map it) |
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
- `gen_ai.provider.name` = `anthropic` on every span
- `gen_ai.operation.name` on every span, from the span name
- `gen_ai.agent.name` = `claude_code` (interaction root)

The connector's `gen_ai.provider.name` and `gen_ai.operation.name` replace
any native copy, so the connector's value and the harness's cannot disagree
in canonical output (see Native canonical keys and precedence above).

## Logs

### Native support

Independent of the traces beta, Claude Code has long exported OTel log
events via `OTEL_LOGS_EXPORTER=otlp` (needs only
`CLAUDE_CODE_ENABLE_TELEMETRY=1`, no beta flag). Structured events include
`claude_code.user_prompt`, `claude_code.tool_result`,
`claude_code.tool_decision`, `claude_code.api_request`,
`claude_code.api_error`, `claude_code.api_refusal`,
`claude_code.mcp_server_connection`, and `claude_code.permission_mode_changed`,
among others such as `claude_code.assistant_response` and the hook, plugin,
skill, and compaction events.
Content stays redacted by default; the operator opts in per field with
`OTEL_LOG_USER_PROMPTS` (prompt text), `OTEL_LOG_ASSISTANT_RESPONSES`
(assistant response text; falls back to `OTEL_LOG_USER_PROMPTS` when unset),
`OTEL_LOG_TOOL_DETAILS` (tool inputs and parameters), and
`OTEL_LOG_RAW_API_BODIES` (raw API request/response bodies as
`api_request_body` / `api_response_body` events) — all default to off. The
related `OTEL_LOG_TOOL_CONTENT` gate is traces-only: it controls the
`tool.output` span event and requires tracing
([monitoring docs, Common configuration variables and Events](https://code.claude.com/docs/en/monitoring-usage#common-configuration-variables),
checked 2026-10-19).

### Connector mapping

Not consumed. The connector's Claude Code edge claims only spans on the
`claude_code.` traces scope (above); it does not claim or normalize Claude
Code's logs signal today.

## Metrics

### Native support

Claude Code emits eight counters under meter `com.anthropic.claude_code`,
enabled with `CLAUDE_CODE_ENABLE_TELEMETRY=1` and
`OTEL_METRICS_EXPORTER=otlp`. Temporality defaults to delta
(`OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative` switches it).
Every metric also carries the standard attributes (`session.id`, `user.id`,
`organization.id`, `terminal.type`, and others, some behind
`OTEL_METRICS_INCLUDE_*` cardinality switches)
([monitoring docs, Metrics](https://code.claude.com/docs/en/monitoring-usage#metrics),
checked 2026-10-19):

| Metric | Unit | Extra attributes |
| --- | --- | --- |
| `claude_code.session.count` | none | `start_type` |
| `claude_code.lines_of_code.count` | none | `type` (`added`/`removed`), `model` |
| `claude_code.pull_request.count` | none | none |
| `claude_code.commit.count` | none | none |
| `claude_code.cost.usage` | USD | `model`, `query_source`, `speed`, `effort`, `agent.name`, `skill.name`, `plugin.name`, `marketplace.name`, `mcp_server.name`, `mcp_tool.name` |
| `claude_code.token.usage` | tokens | `type` (`input`/`output`/`cacheRead`/`cacheCreation`) plus the cost counter's attributes |
| `claude_code.code_edit_tool.decision` | none | `tool_name`, `decision`, `source`, `language` |
| `claude_code.active_time.total` | s | `type` (`user`/`cli`) |

### Connector mapping

Not applicable. The connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (see
`connector/codingagentconnector/factory.go`); it does not process, transform,
or filter OTel metrics for any harness. Claude Code's native metrics, where
exported, pass through a metrics pipeline unmodified, outside this
connector's scope.

## Sources

- Claude Code monitoring docs: https://code.claude.com/docs/en/monitoring-usage (traces, logs, metrics, span attributes; checked 2026-10-19)
- Claude Code Agent SDK observability docs: https://code.claude.com/docs/en/agent-sdk/observability (reachable 2026-10-19; content last verified 2026-08-29)
- [docs/otel-signals.md](../otel-signals.md) — repo-internal signal-support research, refreshed 2026-08-29
- [docs/metrics.md](../metrics.md) — repo-internal metrics instrument catalog, refreshed 2026-08-21 (predates the eight-counter list above)
- Traces attribute matrix above: derived from the edge code and the live
  wire capture of Claude Code 2.1.304 (model `claude-haiku-5-5`), 2026-10-19.
  Rows marked "documented upstream; absent in the 2.1.304 capture" come from
  the monitoring docs' span attribute tables (checked 2026-10-19).
