# Cursor native-log trace synthesis

Status: approved design, not yet implemented. When implemented, the durable
parts of this document move into `docs/design.md`, which tracks the current
system rather than proposals.

## Goal

Extend the logs-to-traces edge so the canonical pipeline covers Cursor's
native OpenTelemetry Export (Enterprise beta): correlate `cursor.*` OTLP log
records into the canonical `invoke_agent`/`chat` vocabulary, keyed on
`cursor.conversation.id`.

Decisions fixed during design review:

- Burst-grain synthesis in a new `internal/cursor` package, mirroring
  `internal/codex`. A "turn" is an activity burst: events with gaps under
  `reorder_window` coalesce, and a quiet window finalizes the trace. Cursor's
  wire has no prompt or completion event, so no closer approximation of a user
  turn exists on this surface.
- Claiming by instrumentation scope `cursor.telemetry`, disjoint from the
  Codex edge's event-name claim.
- Dedupe on `cursor.event.id`, the wire's explicit dedupe key, instead of a
  content fingerprint.
- Fixture-based validation only. Native Cursor OTel is Enterprise-plan and
  server-side configured, and no Enterprise access is available, so the live
  E2E named in `docs/design.md` future work stays blocked; synthetic OTLP
  fixtures authored from the published wire reference replace it.
- No metrics edge. Metric datapoints carry no correlation IDs, so they cannot
  join a conversation; they remain in the raw pipeline untouched.

Non-goals: a metrics-to-traces connector edge, hook-generated traces and the
configurable scope allowlist (separate future work), cross-burst export
dedupe, GitHub Copilot support.

## Research basis

This section reflects the primary wire reference as of 2026-08-21. The
surface is a documented beta and may change before general availability; it
is additive, so the connector must tolerate unknown attributes, event bodies,
and enum values.

Cursor (Anysphere) exports native OTel as an Enterprise-plan beta configured
server-side in Team Settings. OTLP/HTTP binary protobuf only, to `<base>/v1/metrics`
and `<base>/v1/logs`. Instrumentation scope: `cursor.telemetry` / `0.1.0`.
**Metrics and logs only — no distributed traces.**

Resource attributes: `service.name=cursor` (constant), optional
`service.version` (client version on desktop/CLI), `cursor.team.id` (int),
`cursor.surface` (`desktop` | `cli` | `cloud_agent` | `bugbot`),
`cursor.entrypoint` (desktop, cli, web, mobile, sdk_ts, sdk_py, api,
automation, github_pr), optional `cursor.user.id` (opaque team-scoped int).

Metrics (all monotonic delta sums, **no correlation IDs**):

- `cursor.token.usage` (`{token}`) by `cursor.token.type` (input, output,
  cache_read, cache_creation), optional `cursor.model.name`,
  `cursor.api.status`, `cursor.api.billable`.
- `cursor.tool.calls` (`{call}`, value 1 per completed call) by
  `cursor.tool.kind` (builtin, mcp), `cursor.tool.name`, `cursor.tool.status`,
  and `cursor.mcp.server.name` for MCP.
- `cursor.cost.usage` (USD, best-effort estimate) by `cursor.model.name`.

Log events carry the correlation state. Common attributes: `cursor.event.id`
(always; opaque dedupe key, deterministic across retries, worker restarts, and
Kafka replay), `cursor.source_event.id` (always), optional
`cursor.request.id`, optional `cursor.conversation.id` (composer UUID on
IDE/CLI, `bc-...` cloud-agent id; the join key for session reconstruction),
optional `cursor.usage_event.id` (request-grain key on api events, the join
key against usage and billing exports).

| Body | Event | Correlation-bearing attributes |
| --- | --- | --- |
| `api_request` | model request completed | input/output/cache_read/cache_creation token ints (all always), optional model name, optional billable |
| `api_error` | model request errored | optional model name; no raw messages |
| `api_correction_<kind>` | usage event retroactively not billed | `cursor.api.correction.kind`; joins on `cursor.usage_event.id` |
| `skill_activated` | skill entered the context | skill name, trigger, source, optional plugin |
| `hook_execution_complete` | user-configured hook finished | hook name, type, outcome, `duration_ms` |
| `plugin_installed` | plugin install (no conversation id) | plugin name, scope |
| `cloud_agent_pull_request_<kind>` | cloud-agent PR lifecycle | kind, number, draft |
| `cloud_agent_setup_<kind>` | cloud-agent environment setup | kind, optional duration/reason |
| `cloud_agent_artifact_created` | cloud-agent artifact | file name, MIME |
| `cloud_agent_mcp_auth_error` | MCP credentials rejected | MCP server name |

Delivery semantics: logs at-least-once with ~7-day retry (dedupe on
`cursor.event.id`); metrics at-most-once; **no ordering guarantee** —
corrections can arrive after the requests they amend, so order by record
timestamp. No prompt, completion, or tool content exists anywhere on this
wire.

Consequences that shape the design:

1. The wire has no user-prompt or turn-boundary event, so the Codex per-turn
   model does not map; bursts of activity are the closest available grain.
2. Tool calls exist only as metric datapoints without correlation IDs, so
   native telemetry cannot express `execute_tool` children.
3. `cursor.api.request` records carry no durations, so chat spans are point
   spans.
4. The at-least-once, unordered, correction-amends-request shape matches the
   problems the Codex correlation engine already solves.

Primary sources:

- [Cursor OpenTelemetry Export overview](https://cursor.com/docs/enterprise/opentelemetry-export)
- [Cursor OTel wire reference](https://cursor.com/docs/enterprise/opentelemetry-export/wire)
- `docs/harnesses.md` Cursor section (research refreshed 2026-08-20)

## Architecture

A new stateful package `internal/cursor` joins `internal/codex`,
`internal/claude`, and `internal/genai`, with the same file layout as Codex:
`event.go` (parse and claim records), `connector.go` (burst state machine and
sweep loop), `trace.go` (canonical trace construction), self-telemetry
wiring, unit tests, and OTLP fixtures.

The logs-to-traces edge, currently constructed as `codex.New(cfg, set, next)`
in `factory.go`, becomes a claiming router mirroring `tracesRouter`: it holds
both edges and passes the same batch to each. No partitioning or copying; each
edge ignores foreign records, so claims stay disjoint by construction and
unclaimed records never reach the canonical edge (they remain in the parallel
raw pipeline). `Capabilities()` keeps reporting `MutatesData: false`.

The edge ignores records without `cursor.conversation.id`, matching the
existing rule that the connector drops coding-agent logs without a
conversation ID.
This also excludes non-correlatable bugbot records automatically.

## Claiming

Evaluated per log record, inside the Cursor edge:

| Rule | Claim |
| --- | --- |
| Instrumentation-scope name has prefix `cursor.telemetry` | Cursor edge |
| Event name has prefix `codex.` (existing rule, unchanged) | Codex edge |

Scope prefix matching tolerates version-suffixed scope names the way the GenAI
edge's prefix rules do. A record matches at most one rule in practice; the
Codex event-name claim takes precedence in the impossible overlap, and the
Cursor edge additionally skips records with `codex.`-prefixed event names so
the two edges stay disjoint whatever a payload contains.

## Correlation model

### Key and burst boundary

State keys on `cursor.conversation.id` alone, the same single-key shape as
Codex (composer UUIDs and `bc-...` ids are unique in practice). Each record
for a conversation with no active burst opens one; a conversation resumed
after finalization opens a new burst that emits a new trace segment carrying
the same `gen_ai.conversation.id`, matching the canonical guidance that turns
group by conversation id rather than a session-end event.

Finalization reasons:

- `quiet` — no new event for the conversation within `reorder_window` (30s
  default). The normal close: unlike Codex, quiet closes unconditionally
  because the wire has no completion event to require first.
- `timeout` — burst open longer than `turn_timeout` (10m default), the cap on
  pathological bursts; error status, mirroring Codex timeouts.
- `shutdown` — Collector drain.
- `evicted` — LRU eviction past `max_active_turns`.

`reorder_window < turn_timeout` is already enforced by config validation.

### Dedupe and replay

Within a live burst, redelivered records dedupe exactly on
`cursor.event.id` — the wire's own dedupe key, simpler and stronger than the
Codex content fingerprint. The id set bounds at `max_events_per_turn` with
the rest of the burst state.

Trace IDs derive from SHA-256 of `cursor`, the conversation id, and the burst's
first `cursor.event.id`; span IDs add a stable role/event discriminator.
Because `cursor.event.id` is deterministic across retries and Kafka replay, a
full replay of the same burst derives identical IDs and merges idempotently
downstream. A partial replay arriving after finalization can still emit a
fragment trace with a different ID — the same documented Codex limitation
("it does not deduplicate exports by itself"). Cross-burst dedupe state is
out of scope; consumers needing replay-proof reads dedupe on trace ID.

### Timestamps and ordering

Records sort by record timestamp before feeding state (the wire guarantees no
ordering). Timestamp resolution: record timestamp, else observed timestamp,
else wall clock with `coding_agent.timestamp.inferred=true`, matching Codex.

### Bounds

`max_active_turns` bounds concurrent bursts with LRU eviction;
`max_events_per_turn` bounds per-burst memory and sets
`coding_agent.turn.events_truncated=true` past the cap. Identical to Codex.

## Span construction

Emitted tree per burst:

```text
invoke_agent cursor
├── chat <model>        (one point span per cursor.api.request)
└── [no execute_tool — the native wire cannot express them]
```

Root `invoke_agent cursor`: start = first event timestamp, end = last event
timestamp.

- `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name=cursor`,
  `gen_ai.conversation.id`.
- No `gen_ai.provider.name`: the wire never names the upstream model
  provider and the connector does not guess one, consistent with the existing
  stance in `docs/design.md`.
- `coding_agent.client.name=cursor`, `coding_agent.client.version` from
  resource `service.version` when present, `telemetry.source=normalized`.
- `coding_agent.cursor.surface`, `coding_agent.cursor.entrypoint`,
  `coding_agent.cursor.team.id`, `coding_agent.cursor.user.id` from the
  resource. Opaque team-scoped ints and enums with operational value;
  operators who do not want them strip them with a transform processor.
- `coding_agent.turn.finish_reason` in `quiet|timeout|shutdown|evicted`.
  `quiet` is the normal close but the root never claims completion — the
  wire cannot distinguish a finished model turn from an abandoned one, so
  there is no `complete` marker to set. `timeout` sets error status;
  shutdown/evicted leave status unset, mirroring Codex.
- Usage rollup: `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens`
  sum the burst's `api_request` records; cache sums land under
  `gen_ai.usage.cache_read.input_tokens` and
  `gen_ai.usage.cache_creation.input_tokens` (extending the existing
  `cached_token_count` mapping precedent).

Chat children, one per `api_request` record:

- Name `chat <cursor.model.name>`, or bare `chat` when the model attribute is
  absent — the same no-subject-no-suffix rule as the GenAI normalizer.
- Point spans (start = end = record timestamp): the wire has no durations.
- `gen_ai.operation.name=chat`, `gen_ai.request.model` when present,
  `coding_agent.source.event=cursor.api.request`, token attributes from the
  four `cursor.api.request.*_tokens` ints, `coding_agent.cursor.billable`
  when present.
- In-burst `cursor.usage_event.id` joins: an `api_error` sharing the
  request's usage-event id sets that chat span's status to Error; an
  `api_correction` attaching to the same id becomes an event on the matching
  span. When the counterpart arrived in an earlier, already-finalized burst —
  expected, since corrections trail their requests — the event lands on the
  root with the usage-event id preserved for downstream reconciliation.

Root span events, each carrying its safe attributes (names, kinds, outcomes,
durations):

- Unjoined `api_error` and `api_correction` records.
- `skill_activated`, `hook_execution_complete`.
- `cloud_agent_*` lifecycle records (`bc-...` conversations).
- Unknown event bodies (the surface is additive): generic events holding
  only the common id attributes.

Attribute policy is an **allowlist**: the builder copies only the fields
named above from records into spans; everything else stays in the raw
pipeline. This is
the mechanism that keeps canonical output content-free even if Cursor later
adds fields, per the repo's allowlist-over-denylist rule.

## Privacy

The Cursor wire carries no prompt, completion, or tool content by design, so
there is nothing content-bearing to strip today; the attribute allowlist is
what keeps that true as the surface evolves. Skill and hook names —
customer-authored labels — stay in canonical output, the same treatment Codex
tool names get. Raw `cursor.*` logs and metrics flow through the parallel raw pipeline
untouched, with operator-side retention and access policy.

## Configuration and component surface

No new configuration fields, component types, `metadata.yaml` changes, or
self-observability instruments. `Config` remains the alias to the existing
provider-neutral knobs (`turn_timeout`, `reorder_window`, `max_active_turns`,
`max_events_per_turn`); both logs edges consume them with per-edge semantics
documented (Codex quiet-closes only after a completion; Cursor quiet-closes
unconditionally). The Cursor edge reports through the same TelemetryBuilder
instruments (emitted turns by finish reason, dropped duplicate events, active
turns gauge). The logs-to-traces edge keeps its existing development
stability.

## Testing

Unit tests in `internal/cursor`, table-driven and mirroring the Codex suite:

- Parsing and coercion: token int fields, model presence/absence, timestamp
  fallbacks and the inferred marker, unknown bodies and attributes tolerated.
- Burst lifecycle: events within `reorder_window` coalesce; a quiet gap
  closes the burst with reason `quiet`; a resumed conversation emits a new
  segment with the same conversation id; `turn_timeout`, shutdown, and
  eviction close bursts with the right reasons and status.
- Dedupe: a resent batch within a burst drops on `cursor.event.id` without
  double-counting usage or duplicating spans.
- Out-of-order batches, including a correction preceding its request.
- Joins: `api_error` sets chat status Error via `usage_event.id`;
  `api_correction` attaches to the matching span in-burst and to the root
  when the burst no longer holds the request.
- Deterministic IDs: same events in different batch orderings derive the
  same trace and span IDs.
- Fixture replay: the raw fixture fed through the edge in shuffled batch
  orderings matches the canonical fixture (ids, names, attributes, events).
- Bounds: eviction picks the least-recently-seen burst; truncation sets the
  marker.
- No-content assertions over every output span and event.
- Race tests on the sweep-loop/consumer boundary.

Fixtures, following the GenAI `strands-raw`/`strands-canonical` pattern:
`internal/cursor/testdata/cursor-native-logs.json` (synthetic multi-burst
conversation with `api_request`, `api_error`, `api_correction`, skill, hook,
and cloud-agent records, authored from the wire reference) and expected
canonical output JSON.

Fixture replay runs in `internal/cursor` unit tests, which construct the edge
directly (the repo-root e2e module does not depend on the connector module).
The `e2e/validator` package gains assertion helpers for the Cursor canonical
shape, exercised as plain unpaid Go table cases over the canonical fixture —
the fixture path already noted as future work in `docs/design.md`. No new
Compose stack, no live E2E, no CI changes; `./scripts/check.sh` covers
everything added.

## Documentation updates

- Root and connector READMEs: Cursor added to supported sources with its
  limits (session-reconstruction grain, no `execute_tool` children, point
  chat spans, Enterprise-beta source), and the logs pipeline example note
  that one `coding_agent` instance now handles both log sources.
- `docs/design.md`: a Cursor correlation-model section recording the research
  basis and the decisions above when implementation lands; the future-work
  entry drops its "blocked on confirming Cursor's telemetry format" clause
  (confirmed) while the live E2E stays blocked on Enterprise access.
- `docs/harnesses.md` is already current for this surface.

## Known limitations and future work

- No `execute_tool` children and no per-tool attribution: tool calls are
  metrics-only with no correlation IDs. If Cursor ever logs tool calls with
  a conversation id, the edge grows children then.
- Chat spans carry no duration: the wire reports tokens at request grain
  with no timing, and the connector does not invent durations.
- `quiet` finalization is an approximation, not a detected turn end; roots
  never claim completion.
- Partial replays after finalization can emit fragment traces (documented
  Codex limitation, same class).
- Cross-burst dedupe is absent by design; replay-proof consumers dedupe on
  trace ID.
- Fixtures are synthetic. They encode the wire reference as read on
  2026-08-21; the surface is a beta that may change, so refresh fixtures
  against the wire reference before relying on new fields, and a captured
  real fixture remains valuable if Enterprise access ever appears.
- The scope allowlist extension (hook-generated Cursor traces, Pi, and other
  GenAI-scope sources) stays separate future work.
