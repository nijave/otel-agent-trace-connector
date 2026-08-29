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

Last verified: Traces 2026-08-29, Logs 2026-08-27, Metrics 2026-08-21;
export architecture 2026-08-29 (see Sources).

## Export architecture

The export runs server-side: Cursor's cloud aggregates usage and
client-reported events, then pushes OTLP to the team-managed collector through
a server-side egress proxy from six static source addresses (`3.218.161.44`,
`3.231.18.206`, `35.174.159.35`, `184.73.225.134`, `3.209.66.12`,
`52.44.113.131`, all `/32`). The client never pushes OTLP directly. Every
model request already transits Cursor's backend (`api2.cursor.sh`), so the
backend generates the token, cost, and error data server-side; the client
reports hook, plugin, and tool-call events back to Cursor, which folds them
into the same export.

Delivery is signal-dependent:

- **Metrics** are at-most-once; failed metric requests are not retried or
  replayed.
- **Logs** are at-least-once; transient failures retry for about 7 days and
  the wire dedupes on `cursor.event.id`. Cursor honors OTLP partial success
  and does not re-send rejected items.
- **No backfill** from before destination activation; source retention
  upstream of the export is also about 7 days.

Other caveats: `cursor.cost.usage` is a best-effort estimate, not an invoice
(for BYOK it reflects the Cursor Token Rate only, not provider spend);
subagents get their own `cursor.conversation.id` with no parent rollup
exported yet; and the wire carries no model-request timing.

## Traces

### Native support

Cursor has no native traces. The Enterprise-plan native OTel export
(configured server-side under Team Settings → OpenTelemetry Export) covers
metrics and logs only. Traces exist solely through third-party hook tooling;
the connector claims none of it today, and tracks claiming the `o11y-dev`
add-on as a future improvement item (see Connector mapping):

- **`opentelemetry-hooks`** (`o11y-dev`): installs into `~/.cursor/hooks.json`
  and emits GenAI-semconv traces and logs carrying `gen_ai.client.workspace`,
  `gen_ai.client.repository_root`, `vcs.repository.name`,
  `vcs.ref.head.name`, and a SHA-256 hash of a credential-free normalized Git
  remote.
- **`last9/cursorscope`**: a Node hook collector that emits an
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
a client env-var toggle). The scope is `cursor.telemetry`, version `0.1.0`,
with unprefixed record bodies (`api_request`, `api_error`,
`api_correction_<kind>`, …); the `cursor.*` namespace carries attributes.
Transport is OTLP/HTTP binary protobuf to `/v1/logs` only; the wire supports
neither gRPC nor JSON. Coverage spans desktop, CLI, cloud_agent, and bugbot
surfaces via `cursor.surface`. Delivery is at-least-once, deduplicated on
`cursor.event.id`.

### Connector mapping

The connector is a logs-to-traces edge: it deduplicates redelivered
records, correlates each burst of records for a conversation into a
deterministic trace, and emits one `invoke_agent cursor` root with a
`chat <model>` child per `api_request`. Records the chat spans do not
consume (unjoined errors and corrections, skill/hook/cloud-agent lifecycle
records) land on the root as span events carrying **name and timestamp
only**.

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
| `cursor.model.name` | `gen_ai.request.model` | mapped (also names the span; the wire marks the model optional, so a request without it yields a bare `chat` span with no key) |
| `cursor.conversation.id` | `gen_ai.conversation.id` | mapped (on the root; also the burst key) |
| `cursor.api.billable` → `coding_agent.cursor.billable` | — | dropped (billing detail outside the vocabulary) |
| `cursor.event.id` | — | dropped (wire dedupe key; consumed internally) |
| `cursor.usage_event.id` | — | dropped (join key to error/correction records; consumed internally) |
| `cursor.source_event.id` | — | dropped |
| any duration/latency field | — | not provided (the wire carries no model-request timing ⇒ no TTFT source; the hook/setup duration fields ride non-chat records) |

##### invoke_agent cursor root

| Raw key | Canonical key | Status |
|---|---|---|
| `service.version` (resource) | `coding_agent.client.version` | mapped (desktop/CLI clients only; cloud-agent and bugbot resources carry no `service.version`) |
| `conversation.id` | `gen_ai.conversation.id` | mapped |
| first event body | `coding_agent.source.event` | mapped |
| `cursor.surface`, `cursor.entrypoint` (resource) | — | dropped |
| `cursor.user.id` (resource) | `coding_agent.user.id` | mapped (only when `capture_identity` is on) |
| `cursor.team.id` (resource) | `coding_agent.team.id` | mapped (only when `capture_identity` is on) |
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
| `gen_ai.tool.*` / `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
| `server.address` / `server.port` | not provided |
| `exception.*` | not provided (errors surface as chat span Status Error) |

## Metrics

### Native support

Native, from the same Enterprise-plan beta, over OTLP/HTTP to
`/v1/metrics`, delta temporality. Per [docs/metrics.md](../metrics.md),
three monotonic delta Sums:

| Metric | Type | Attributes |
|---|---|---|
| `cursor.token.usage` | Monotonic delta Sum, `{token}` | `cursor.token.type` (Always: input/output/cache_read/cache_creation), `cursor.model.name` (Optional, routed-intent collapsed), `cursor.api.status` (Optional: success/errored/aborted), `cursor.api.billable` (Optional bool) |
| `cursor.tool.calls` | Monotonic delta Sum, `{call}`, value 1 | `cursor.tool.kind` (Always: builtin/mcp), `cursor.tool.name` (Always), `cursor.tool.status` (Always: success/failure/aborted), `cursor.mcp.server.name` (MCP only) |
| `cursor.cost.usage` | Monotonic delta Sum, USD | `cursor.model.name` (Optional, same collapse rules) |

`cursor.team.id` rides as a resource attribute, not a datapoint attribute,
and metrics carry no `cursor.session.id`. Attributing a datapoint to a
conversation requires joining `cursor.api.request` logs on
`cursor.conversation.id`.

### Connector mapping

Not applicable — the connector only registers `logs-to-traces` and
`traces-to-traces` connector pipelines (connector/codingagentconnector/factory.go);
it does not process OTel metrics for any harness.

## Sources

- Cursor OpenTelemetry Export: https://cursor.com/docs/enterprise/opentelemetry-export
- Cursor OTel Wire Reference: https://cursor.com/docs/enterprise/opentelemetry-export/wire
- Cursor hook tooling: https://github.com/o11y-dev/opentelemetry-hooks,
  https://github.com/last9/cursorscope
- Cursor proxy architecture (client → `api2.cursor.sh`):
  https://speedscale.com/blog/peeking-under-the-hood-of-cursor/
- [docs/otel-signals.md](../otel-signals.md) — signal-support summary, refreshed 2026-08-29
- [docs/metrics.md](../metrics.md) — Cursor metrics catalog, refreshed 2026-08-21
- [docs/harnesses.md](../harnesses.md) — Cursor research record, refreshed 2026-08-20/21
