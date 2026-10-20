# Codex — OpenTelemetry signal reference

Codex exports structured OTLP **logs** (`codex.*` events with
`conversation.id`). With `otel.trace_exporter` configured it also exports
native trace spans, but the connector does not consume them. The connector
is a logs-to-traces edge: it deduplicates redelivered records, correlates each
turn's events into a deterministic trace, and emits one `invoke_agent codex`
root with `chat <model>` and `execute_tool <tool>` children plus the
remaining raw events attached to the root as span events (names only). See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy behind it.

## Signal support

| Signal | Native support | Connector support |
| --- | --- | --- |
| Traces | native, opt-in (`otel.trace_exporter`, default `none`) | none (no traces edge for Codex spans; the Logs edge synthesizes the canonical trace) |
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

The e2e config sets only the log `exporter`, so the pinned capture has no
native spans.

### Connector mapping

None. The connector has no traces edge for Codex's native spans, and the
canonical `invoke_agent codex` trace does not use them. See
**Logs → Connector mapping** for the synthesis this connector performs
instead.

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
| duplicate timing-only `response.completed` (no `ttft_ms`, no token counts), including a failed completion that carries only `error.message` | dropped |
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
When the provider sends no `usage`, Codex logs only the timing-only record,
which the connector drops, so that model call gets no chat span. Codex 0.163.0
speaks only the Responses wire API. Sources (rust-v0.163.0, checked
2026-10-19): [`core/src/client.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/core/src/client.rs#L2572-L2580),
[`codex-api/src/sse/responses.rs`](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/codex-api/src/sse/responses.rs#L119-L145),
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
| `input_token_count` | `gen_ai.usage.input_tokens` | mapped |
| `output_token_count` | `gen_ai.usage.output_tokens` | mapped |
| `cached_token_count` | `gen_ai.usage.cache_read.input_tokens` | mapped (0, not absent, when the provider's usage has no `input_tokens_details`) |
| `cache_write_token_count` | `gen_ai.usage.cache_write.input_tokens` | mapped (0, not absent, when the provider's usage has no cache-write field; the pinned 0.163.0 capture reports it non-zero — 7924 then 105 across the two completions) |
| `tool_token_count` | `gen_ai.usage.total_tokens` | mapped (Codex logs the provider's `usage.total_tokens` under this name; the connector never computes a total) |
| `reasoning_token_count` | `gen_ai.usage.reasoning.output_tokens` | mapped (0, not absent, when the provider's usage has no `output_tokens_details`; replaces the former vendor `coding_agent.usage.reasoning_tokens`) |
| `ttft_ms` | `gen_ai.response.time_to_first_chunk` | mapped (integer ms → seconds, double; absent when Codex saw no `response.output_item.added` before the completion — either `ttft_ms` or a token count keeps a completion, and a record with neither drops as the timing-only duplicate) |
| `model` | `gen_ai.request.model` | mapped (also names the span) |
| `model_reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (the request's effort, not a provider value; absent when neither config nor the model's default sets one — Codex sends an effort of `none` as the string `none`; the pinned 0.163.0 capture carries `low`) |
| `service_tier` | `coding_agent.request.service_tier` | mapped (the request's tier, not a provider value; absent when the request sets no service tier, as in the pinned 0.163.0 capture) |
| `duration_ms` | — | dropped (used for span bounds only) |
| `event.kind`, `event.timestamp` | — | dropped (span name/bounds carry them) |
| `slug`, `originator`, `terminal.type`, `auth_mode`, `error.message` | — | dropped |

##### codex.tool_result → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `tool_name` | `gen_ai.tool.name` | mapped (also names the span; a record without `tool_name` gets the literal `unknown`) |
| `success` = false | span Status = Error, message `tool execution failed` | mapped to status (the connector drops the boolean attribute itself) |
| `call_id` | — | dropped (the connector does not emit `gen_ai.tool.call.id`) |
| `duration_ms` | — | dropped (used for span bounds only) |
| `arguments`, `output` | — | dropped (tool content, stripped at parse time) |
| `mcp_server`, `mcp_server_origin` | — | dropped |
| `tool_namespace` | — | dropped (tool provenance detail, outside the usage/cost/performance scope) |
| `tool_result_seq` | — | dropped (process-wide result counter, not a per-turn ordinal) |
| `output_truncated` | — | dropped (qualifies tool output content that the connector strips at parse time) |
| `agent_name` | — | dropped (agent identity: `/root` for the main agent, as in the pinned capture; for a subagent, its agent path, its nickname, or its conversation UUID; per-subagent `invoke_agent` spans would be the semconv-shaped path if this is ever wanted) |
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
| `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `gen_ai.tool.call.id` / `gen_ai.tool.type` / `gen_ai.tool.status` | not provided |
| `server.address` / `server.port` | not provided (proxied setups are indistinguishable) |
| `exception.*` | not provided |

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
  `config/src/types.rs`, `config/src/loader/mod.rs`, `core/src/otel_init.rs`,
  `core/src/client.rs`)
- Codex 0.163.0 release notes: https://github.com/openai/codex/releases/tag/rust-v0.163.0 (checked 2026-10-19)
- Codex advanced configuration docs: https://developers.openai.com/codex/config-advanced (project-local `otel` ignored; checked 2026-10-19)
- [docs/otel-signals.md](../otel-signals.md) — top-level per-signal matrix (refreshed 2026-08-29; its Codex traces row predates the native traces finding above)
- [docs/harnesses.md](../harnesses.md) — summary table (refreshed 2026-08-20; its Codex row predates the native traces finding above)
- [docs/metrics.md](../metrics.md) — full Codex instrument catalog (refreshed 2026-08-21, verified against upstream clone commit `4f39251`)
- Wire capture behind the Logs attribute matrix above: Codex 0.163.0 (`codex exec`, API-key auth, Responses API), captured and audited 2026-10-19 against the `rust-v0.163.0` tag
