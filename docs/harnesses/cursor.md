# Cursor — raw → canonical attribute matrix

Cursor emits OTLP **logs** on scope `cursor.telemetry` with unprefixed record
bodies (`api_request`, `api_error`, `api_correction_<kind>`, …); the `cursor.*`
namespace carries attributes. The connector is a logs-to-traces edge: it
deduplicates redelivered records, correlates each burst of records for a
conversation into a deterministic trace, and emits one `invoke_agent cursor`
root with a `chat <model>` child per `api_request`. Records the chat spans do
not consume (unjoined errors and corrections, skill/hook/cloud-agent lifecycle
records) land on the root as span events carrying **name and timestamp only**.
See [canonical attributes](../canonical-attributes.md) for the shared
vocabulary and the policy behind it.

Event-to-span mapping:

| Raw log event | Canonical output |
|---|---|
| `api_request` | `chat <model>` span |
| `api_error` joined to an in-burst `api_request` | chat span Status = Error |
| `api_error` whose request sat in an earlier burst | root span event (name only) |
| `api_correction_<kind>` joined to an in-burst request | chat span event named `api_correction_<kind>` (name only) |
| `api_correction_<kind>`, unjoined | root span event (name only) |
| `skill_activated`, `hook_execution_complete`, `cloud_agent_*` | root span events (name only) |
| `plugin_installed` | never emitted (the record carries no conversation id, so the standing no-conversation-id policy declines it at parse) |

**Event names survive.** Correction bodies embed their kind, so
`api_correction_not_billed_errored` stays fully readable as a span-event name
even after its detail attributes drop.

## Attribute matrix

Status is one of **mapped** (remapped onto the canonical key; a parenthetical
names any wire condition under which the key is absent), **dropped**
(deliberately removed from canonical output; recoverable only via a raw
preservation pipeline branch), or **not provided** (the source never emits a
raw key that would map there).

### api_request → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `cursor.api.request.input_tokens` | `gen_ai.usage.input_tokens` | mapped |
| `cursor.api.request.output_tokens` | `gen_ai.usage.output_tokens` | mapped |
| `cursor.api.request.cache_read_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped |
| `cursor.api.request.cache_creation_tokens` | `gen_ai.usage.cache_write.input_tokens` | mapped |
| `cursor.model.name` | `gen_ai.request.model` | mapped (also names the span; the wire marks the model optional, so a request without it yields a bare `chat` span with no key) |
| `cursor.conversation.id` | `gen_ai.conversation.id` | mapped (on the root; also the burst key) |
| `cursor.api.billable` → `coding_agent.cursor.billable` | — | dropped (billing detail outside the vocabulary) |
| `cursor.event.id` | — | dropped (wire dedupe key; consumed internally) |
| `cursor.usage_event.id` | — | dropped (join key to error/correction records; consumed internally) |
| `cursor.source_event.id` | — | dropped |
| any duration/latency field | — | not provided (the wire carries no model-request timing ⇒ no TTFT source; the hook/setup duration fields ride non-chat records) |

### invoke_agent cursor root

| Raw key | Canonical key | Status |
|---|---|---|
| `service.version` (resource) | `coding_agent.client.version` | mapped (desktop/CLI clients only; cloud-agent and bugbot resources carry no `service.version`) |
| `conversation.id` | `gen_ai.conversation.id` | mapped |
| first event body | `coding_agent.source.event` | mapped |
| `cursor.surface`, `cursor.entrypoint` (resource) | — | dropped |
| `cursor.team.id`, `cursor.user.id` (resource) | — | dropped |
| turn-total usage rollup (summed token counts on the root) | — | dropped (usage lives on chat spans only; sum them for turn totals) |
| connector close reason (`quiet`/`timeout`/`evicted`/`shutdown`) | — | dropped (was **connector-derived**, never a model stop reason; timeouts surface as root span Status Error and every close shows up in the `otelcol_coding_agent_turns_emitted` metric's `finish_reason` label) |
| events-truncated flag | — | dropped (still exposed via `otelcol_coding_agent_turns_truncated`) |

### Root/chat span events (errors, corrections, skill/hook/cloud-agent records)

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

## Connector-written attributes

The connector writes these itself rather than remapping them from raw keys;
they appear on every emitted span except where noted:

- `coding_agent.source` = `normalized`
- `coding_agent.client.name` = `cursor`
- `coding_agent.source.event` = originating record body (`api_request`, …)
- `gen_ai.operation.name` = `invoke_agent` / `chat`
- `gen_ai.agent.name` = `cursor` (invoke_agent root only)

## Canonical keys with no Cursor source

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (no model-request timing on the wire) |
| `gen_ai.response.finish_reasons` | not provided (Cursor logs no model stop reason) |
| `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `gen_ai.usage.total_tokens` / `gen_ai.usage.reasoning.output_tokens` | not provided |
| `gen_ai.provider.name` | not provided (the wire never names the upstream provider; the connector does not guess one) |
| `gen_ai.request.max_tokens` / `gen_ai.request.stream` | not provided |
| `gen_ai.tool.*` / `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `server.address` / `server.port` | not provided |
| `exception.*` | not provided (errors surface as chat span Status Error) |
