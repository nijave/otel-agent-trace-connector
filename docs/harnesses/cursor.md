# Cursor — OpenTelemetry signal reference

Cursor (Anysphere) exports OpenTelemetry from the Enterprise plan,
configured server-side by admins rather than by a client env var. The wire
carries metrics and logs only — no native traces — and the connector claims
the logs signal, synthesizing canonical traces from it. See
[canonical attributes](../canonical-attributes.md) for the shared vocabulary
and the policy the connector's mapping follows.

## Signal support

| Signal  | Native support | Connector support |
| ------- | -------------- | ------------------ |
| Traces  | none native (hook tooling only) | n/a (traces synthesized from Logs edge) |
| Logs    | native (Enterprise plan) | logs edge (this file's Connector mapping) |
| Metrics | native (Enterprise plan) | not applicable |

Last verified: Traces, Logs, Metrics, and export architecture 2026-10-19
(see Sources).

## Export architecture

The export runs server-side: Cursor's cloud aggregates usage and
client-reported events, then pushes OTLP to the team-managed collector through
a server-side egress proxy from a fixed set of static `/32` source addresses
(27 as of 2026-10-19; the OpenTelemetry Export page lists them under Source
IPs, and they do not rotate without advance notice). The client never pushes
OTLP directly. Every
model request already transits Cursor's backend (`api2.cursor.sh`), so the
backend generates the token, cost, and error data server-side; the client
reports hook, plugin, and tool-call events back to Cursor, which folds them
into the same export.

Delivery is signal-dependent:

- **Metrics** are at-most-once; failed metric requests are not retried or
  replayed.
- **Logs** are at-least-once; transient failures retry for about 7 days and
  the wire dedupes on `cursor.event.id`. Cursor honors OTLP partial success
  and does not re-send rejected items. Records carry no ordering guarantee:
  a correction can arrive after the request it amends.
- **No backfill** from before destination activation; source retention
  upstream of the export is also about 7 days.

Other caveats: `cursor.cost.usage` is a best-effort estimate, not an invoice
(for BYOK it reflects the Cursor Token Rate only, not provider spend);
subagents get their own `cursor.conversation.id`, and outside Grok Bot
(whose action records carry `cursor.grok_bot.root_turn.id`) Cursor exports
no parent rollup yet; and the wire carries no model-request timing.

## Traces

### Native support

Cursor has no native traces. The Enterprise-plan native OTel export
(configured server-side under Team Settings → OpenTelemetry Export) covers
metrics and logs only. Traces exist solely through third-party hook tooling;
the connector claims none of it today, and tracks claiming the `o11y-dev`
add-on as a future improvement item (see Connector mapping):

- **`opentelemetry-hooks`** (`o11y-dev`, v0.15.0): installs into
  `~/.cursor/hooks.json` (or a project's `.cursor/hooks.json`) and emits GenAI-semconv traces and logs carrying `gen_ai.client.workspace`,
  `gen_ai.client.repository_root`, `vcs.repository.name`,
  `vcs.ref.head.name`, and a SHA-256 hash of a credential-free normalized Git
  remote.
- **`last9/cursorscope`** (v0.3.7): a Node hook collector that emits an
  `invoke_agent Cursor` span shape carrying `cursor.repo`,
  `gen_ai.request.model`, and `gen_ai.conversation.id` attributes.

See [docs/otel-signals.md](../otel-signals.md) and
[docs/harnesses.md](../harnesses.md) for the cross-harness research behind
this finding.

### Connector mapping

Not applicable today — no native trace signal exists to normalize; the
connector builds canonical traces from the Logs edge below (see Logs).
Future improvement: claim the `o11y-dev/opentelemetry-hooks` add-on through a
hooks-collection pipeline and normalize its GenAI-semconv traces (which
already carry repo identity attributes) onto the canonical vocabulary. The
`last9/cursorscope` span shape stays out of scope.

## Logs

### Native support

Cursor emits OTLP **logs** natively, from the same Enterprise plan,
configured server-side only under Team Settings → OpenTelemetry Export (not
a client env-var toggle). The scope is `cursor.telemetry`, version `0.1.0`.
Event names take the form `cursor.api.request`, `cursor.api.error`, … and most
record bodies are an unprefixed constant (`api_request`, `api_error`,
`api_correction_<kind>`, `grok_bot_<action>`, …); the `cursor.*` namespace
carries attributes. The exception is the opt-in `conversation_content`
family (`cursor.conversation.user_message`, `assistant_message`,
`tool_io`; off by default, Cloud Agents and Grok Bot only), whose body is
the scrubbed message text or one side of an MCP tool call. Admins toggle
each family (`model_usage`, `tool_calls`, `skills_hooks_plugins`,
`cloud_agents`, `grok_bot_agent_actions`, `conversation_content`)
per destination. Transport is OTLP/HTTP binary protobuf to `/v1/logs` only;
the wire supports neither gRPC nor JSON. Coverage spans the `desktop`,
`cli`, `cloud_agent`, `bugbot`, and `grok_bot` surfaces via
`cursor.surface`. Delivery is at-least-once, deduplicated on
`cursor.event.id`; exported logs carry no OTel `trace_id` or `span_id`.

### Connector mapping

The connector is a logs-to-traces edge: it claims records whose scope
starts with `cursor.telemetry` and that carry `cursor.conversation.id`,
deduplicates redelivered records on `cursor.event.id` while their burst is
still open (a redelivery that arrives after the burst closed starts a new
burst and trace), correlates each burst of records for a conversation into a
deterministic trace, and emits one `invoke_agent cursor` root with a
`chat <model>` child per `api_request`. Records the chat spans do not
consume (unjoined errors and corrections, skill/hook/cloud-agent/Grok Bot
records, conversation content) land on the root as span events carrying
**name and timestamp only**. The event name is the record body, not the
OTLP event name.

Event-to-span mapping:

| Raw log event | Canonical output |
|---|---|
| `api_request` | `chat <model>` span |
| `api_error` joined to an in-burst `api_request` | chat span Status = Error |
| `api_error` whose request sat in an earlier burst | root span event (name only) |
| `api_correction_<kind>` joined to an in-burst request | chat span event named `api_correction_<kind>` (name only) |
| `api_correction_<kind>`, unjoined | root span event (name only) |
| `skill_activated`, `hook_execution_complete`, `cloud_agent_*`, `grok_bot_*` | root span events (name only) |
| `cursor.conversation.*` (opt-in; body is the scrubbed message or tool I/O text) | root span event named with that body text (the edge reads the body, not the OTLP event name) |
| `plugin_installed` | never emitted (the record carries no conversation id, so the standing no-conversation-id policy declines it at parse) |

**Event names survive.** Correction bodies embed their kind, so
`api_correction_not_billed_errored` stays fully readable as a span-event name
even after its detail attributes drop.

#### Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

##### api_request → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `cursor.api.request.input_tokens` | `gen_ai.usage.input_tokens` | mapped |
| `cursor.api.request.output_tokens` | `gen_ai.usage.output_tokens` | mapped |
| `cursor.api.request.cache_read_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped |
| `cursor.api.request.cache_creation_tokens` | `gen_ai.usage.cache_write.input_tokens` | mapped |
| `cursor.model.name` | `gen_ai.request.model` | mapped (absent when the wire omits the optional model; also names the span, which then falls back to bare `chat`) |
| `cursor.conversation.id` | `gen_ai.conversation.id` | mapped (on the root; also the burst key) |
| `cursor.api.billable` → `coding_agent.cursor.billable` | — | dropped (billing detail outside the vocabulary) |
| `cursor.event.id` | — | dropped (wire dedupe key; consumed internally) |
| `cursor.usage_event.id` | — | dropped (join key to error/correction records; consumed internally) |
| `cursor.source_event.id` | — | dropped |
| `cursor.request.id` | — | dropped |
| `cursor.grok_bot.turn.id` (`grok_bot` surface only) | — | dropped |
| any duration/latency field | — | not provided (the wire carries no model-request timing ⇒ no TTFT source; the hook/setup duration fields ride non-chat records) |

##### invoke_agent cursor root

| Raw key | Canonical key | Status |
|---|---|---|
| `service.name` (resource) | `service.name` (resource) | mapped (kept as a canonical resource key; constant `cursor`) |
| `service.version` (resource) | `coding_agent.client.version` | mapped (absent when the resource carries no `service.version`, which is usual on cloud-agent and bugbot resources; desktop/CLI clients send it) |
| `cursor.conversation.id` | `gen_ai.conversation.id` | mapped |
| first event body | `coding_agent.source.event` | mapped (on an opt-in `cursor.conversation.*` record the body is message or tool I/O text) |
| `cursor.surface`, `cursor.entrypoint` (resource) | — | dropped |
| `cursor.user.id` (resource) | `coding_agent.user.id` | mapped (absent when `capture_identity` is off or the resource has no user, e.g. a cloud-agent run started with a team API key or service account) |
| `cursor.team.id` (resource) | `coding_agent.team.id` | mapped (absent when `capture_identity` is off) |
| `cursor.user.email` (resource) | — | dropped (the edge does not map it onto the canonical `coding_agent.user.email`) |
| `cursor.user.account_id` (resource) | — | dropped |
| turn-total usage rollup (summed token counts on the root) | — | dropped (usage lives on chat spans only; sum them for turn totals) |
| connector close reason (`quiet`/`timeout`/`evicted`/`shutdown`) | — | dropped (was **connector-derived**, never a model stop reason; timeouts surface as root span Status Error and every close shows up in the `otelcol_coding_agent_turns_emitted` metric's `finish_reason` label) |
| events-truncated flag | — | dropped (still exposed via `otelcol_coding_agent_turns_truncated`) |

##### Root/chat span events (errors, corrections, skill/hook/cloud-agent records)

Every event here used to carry `coding_agent.cursor.*` copies of its wire
attributes; each copy now drops and the events keep names and timestamps
only:

| Former attribute | Source raw key | Status |
|---|---|---|
| `coding_agent.cursor.event_id` | `cursor.event.id` | dropped (dedupe key, consumed internally) |
| `coding_agent.cursor.model` | `cursor.model.name` (on `api_error`) | dropped |
| `coding_agent.cursor.usage_event_id` | `cursor.usage_event.id` | dropped (join key, consumed internally) |
| `coding_agent.cursor.correction.kind` | `cursor.api.correction.kind` | dropped (kind survives in the event name: `api_correction_<kind>`) |
| `coding_agent.cursor.skill.{name,trigger,source}` | `cursor.skill.*` | dropped (event name `skill_activated` survives) |
| `coding_agent.cursor.plugin.name` | `cursor.plugin.name` | dropped (`plugin_installed` records never emit at all — no conversation id; the optional plugin attribute on `skill_activated` drops with that event name surviving) |
| `coding_agent.cursor.hook.{name,type,outcome,duration_ms}` | `cursor.hook.*` | dropped (event name `hook_execution_complete` survives) |
| `coding_agent.cursor.pull_request.{kind,number,draft}` | `cursor.cloud_agent.pull_request.*` | dropped (cloud-agent event name survives) |
| `coding_agent.cursor.setup.{kind,duration_ms,reason}` | `cursor.cloud_agent.setup.*` | dropped (cloud-agent event name survives) |
| `coding_agent.cursor.artifact.{file_name,content_type}` | `cursor.cloud_agent.artifact.*` | dropped (cloud-agent event name survives) |
| `coding_agent.cursor.mcp.server.name` | `cursor.mcp.server.name` | dropped (cloud-agent event name survives) |

Attributes that never had a `coding_agent.cursor.*` copy drop the same way,
leaving only the event name: `cursor.api.billable` on `api_error`,
`cursor.plugin.name` on `hook_execution_complete`, the `cloud_agent_commit`
and `cloud_agent_review_followup` attributes (`vcs.*`, `cursor.commit.*`,
`cursor.review.*`, `cursor.parent.conversation.id`,
`cursor.bugbot.finding.id`), every `cursor.grok_bot.*` attribute, and the
`cursor.conversation.*` content attributes.

#### Connector-written attributes

The connector writes these itself rather than remapping them from raw keys;
they appear on every emitted span except where noted:

- `coding_agent.source` = `normalized`
- `coding_agent.client.name` = `cursor`
- `coding_agent.source.event` = originating record body (`api_request`, …)
- `gen_ai.operation.name` = `invoke_agent` / `chat`
- `gen_ai.agent.name` = `cursor` (invoke_agent root only)

#### Canonical keys with no Cursor source

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (no model-request timing on the wire) |
| `gen_ai.response.finish_reasons` | not provided (Cursor logs no model stop reason) |
| `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `gen_ai.usage.total_tokens` / `gen_ai.usage.reasoning.output_tokens` | not provided |
| `gen_ai.provider.name` | not provided (the wire never names the upstream provider; the connector does not guess one) |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `gen_ai.request.reasoning.level` / `coding_agent.request.service_tier` | not provided |
| `gen_ai.event.start_time` / `gen_ai.event.end_time` | not provided |
| `coding_agent.terminal.type` | not provided |
| `gen_ai.tool.*` / `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `server.address` / `server.port` | not provided |
| `exception.*` | not provided (errors surface as chat span Status Error) |

## Metrics

### Native support

Native, from the same Enterprise-plan export, over OTLP/HTTP to
`/v1/metrics`, delta temporality. Per [docs/metrics.md](../metrics.md),
three monotonic delta Sums:

| Metric | Type | Attributes |
|---|---|---|
| `cursor.token.usage` | Monotonic delta Sum, `{token}` | `cursor.token.type` (Always: input/output/cache_read/cache_creation), `cursor.model.name` (Optional, routed-intent collapsed), `cursor.api.status` (Optional: success/errored/aborted), `cursor.api.billable` (Optional bool) |
| `cursor.tool.calls` | Monotonic delta Sum, `{call}`, value 1 | `cursor.tool.kind` (Always: builtin/mcp), `cursor.tool.name` (Always), `cursor.tool.status` (Always: success/failure/aborted), `cursor.mcp.server.name` (MCP only) |
| `cursor.cost.usage` | Monotonic delta Sum, USD | `cursor.model.name` (Optional, same collapse rules) |

`cursor.team.id` rides as a resource attribute, not a datapoint attribute,
and datapoints carry no correlation ids (no conversation, request, or usage
event id). Per-conversation analysis uses the `cursor.api.request` logs,
grouped on `cursor.conversation.id`.

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (connector/codingagentconnector/factory.go);
it does not process OTel metrics for any harness.

## Sources

- Cursor OpenTelemetry Export (source IPs, families, conversation content,
  delivery, limitations): https://cursor.com/docs/enterprise/opentelemetry-export
  — checked 2026-10-19
- Cursor OTel Wire Reference (resource attributes, per-event bodies and
  presence rules, delivery semantics):
  https://cursor.com/docs/enterprise/opentelemetry-export/wire — checked
  2026-10-19
- Cursor hook tooling:
  https://github.com/o11y-dev/opentelemetry-hooks/tree/v0.15.0 (`setup.sh`,
  `README.md`),
  https://github.com/last9/cursorscope/tree/v0.3.7 (`src/gen-ai-semconv.js`,
  `src/telemetry.js`) — checked 2026-10-19
- Cursor proxy architecture (client → `api2.cursor.sh`):
  https://speedscale.com/blog/peeking-under-the-hood-of-cursor/ — checked
  2026-10-19
- [docs/otel-signals.md](../otel-signals.md) — signal-support summary, refreshed 2026-08-29
- [docs/metrics.md](../metrics.md) — Cursor metrics catalog, refreshed 2026-08-21
- [docs/harnesses.md](../harnesses.md) — Cursor research record, refreshed 2026-08-20/21
