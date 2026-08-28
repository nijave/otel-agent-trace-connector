# Design and assumptions

This document records the connector's current design and updates with the
implementation rather than serving as a future proposal.

## Use case

The connector serves organization-level visibility into AI coding-agent
usage while respecting developer privacy. Token usage, and the cost
computed from it, is the primary measurement; performance (turn duration,
time-to-first-token, error status) comes second. Privacy constrains both:
canonical output answers what agent activity consumed and how it performed
without carrying what developers typed, what tools received, or what tools
returned.

Cost computation happens downstream. Canonical spans carry token counts,
including the cache and reasoning splits that price differently, and
pricing joins happen at query time, so historical traces stay correct as
provider prices change. The
[canonical attribute vocabulary](canonical-attributes.md) scopes itself to
exactly this: usage, cost, and performance, uniform across harnesses.

## Goals

1. Produce a comparable trace per user turn — or the nearest boundary each
   wire supports — across all supported harnesses, so usage and
   performance questions span agents in one query.
2. Keep vendor telemetry in a parallel raw pipeline.
3. Keep provider mappings explicit and testable.
4. Bound memory and latency under missing, duplicated, delayed, or malformed events.
5. Keep prompt text, tool arguments, and tool output out of canonical
   output unconditionally; recommended harness defaults keep them off the
   wire entirely.
6. Package the component independently and compose it into a pinned Collector
   distribution with OCB.

The connector does not fork `opentelemetry-collector-contrib`, and it does not
expect runtime plugin loading. Component changes require a new OCB build. The
connector's module directory carries the name `codingagentconnector` (a valid Go
package identifier), so OCB derives the import alias automatically and the
manifest needs no explicit `name`.

## Research basis

Research reflects primary documentation and source as of 2026-07-11.

Codex officially exports structured OTLP logs for conversation start, API,
SSE/WebSocket, user prompt, tool-decision, and tool-result activity. Shared
metadata includes `conversation.id`, model, client version, and timestamps.
`codex.sse_event` with `event.kind=response.completed` logs twice per model
call, from two emission sites: the SSE frame handler reports only `duration_ms`,
while turn completion reports `ttft_ms` plus whatever token counts the provider
returned. Codex measures both timing fields, so `ttft_ms` distinguishes the
two records even for providers that report no usage at all.
The source also confirms tool correlation through `call_id` and durations in
milliseconds. User prompts, tool arguments, and outputs are content-bearing and
are deliberately excluded from generated spans.

Claude Code's beta trace exporter already creates one
`claude_code.interaction` root per prompt, with `claude_code.llm_request` and
`claude_code.tool` descendants. Rebuilding that hierarchy from its logs would
discard native trace context, span links, subagent nesting, and tool-execution
detail. The traces-to-traces path copies the pdata batch, preserves
all IDs and hierarchy, and adds canonical naming and attributes.

Research reflects primary sources as of 2026-08-19 for the GenAI semconv extension.

`opentelemetry-instrumentation-openai-v2` wraps the OpenAI Python SDK and has
two modes:

- Default mode (semconv v1.30.0, scope
  `opentelemetry.instrumentation.openai_v2`): CLIENT spans named
  `chat {model}` and `embeddings {model}` with `gen_ai.operation.name`,
  `gen_ai.system=openai`, `gen_ai.request.model`, request parameters,
  `server.address`/`server.port`, and response-side `gen_ai.response.model`,
  `gen_ai.response.id`, `gen_ai.response.finish_reasons`,
  `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`. Prompt and
  completion content goes to log events only, off by default.
- Experimental mode (`OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental`,
  semconv v1.37.0): the `opentelemetry-util-genai` package emits the spans
  (scope `opentelemetry.util.genai.handler`), uses
  `gen_ai.provider.name=openai`, adds Responses API coverage (a
  `fetch_response` operation), and can place content on span attributes
  (`gen_ai.input.messages`, `gen_ai.output.messages`) under the
  `span_only`/`event_only`/`span_and_event` capture modes. `opentelemetry-util-genai` also exposes inference, embedding,
  tool, workflow, and local/remote agent invocations, so hand-rolled agents
  can emit the full `invoke_agent`/`chat`/`execute_tool` vocabulary through
  the same scope.

Strands Agents SDK has a built-in tracer (scope
`strands.telemetry.tracer`). Span names are already semconv-shaped:
`invoke_agent {agent_name}`, `execute_event_loop_cycle`, `chat` (no model
suffix; model in `gen_ai.request.model`), `execute_tool {tool_name}`,
`invoke_swarm`/`invoke_graph`, and `memory.*`. Strands emits token usage
under both legacy (`gen_ai.usage.prompt_tokens`/`completion_tokens`) and
current (`input_tokens`/`output_tokens`) keys plus `total_tokens` and cache
read/write counts. Strands captures content by default as span events
(`gen_ai.user.message`, `gen_ai.choice`, `gen_ai.system.message` legacy;
`gen_ai.client.inference.operation.details` carrying
`gen_ai.input.messages`/`gen_ai.output.messages` latest); the
`gen_ai_span_attributes_only` token copies that content onto span
attributes, and execute_tool spans under latest conventions record
`gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` as span
attributes. Redaction is opt-in via the
`gen_ai_unredacted_attributes=<list>` token; absent the token, Strands
exports content unredacted.

Upstream renamed the package to `opentelemetry-instrumentation-genai-openai`
in the new `opentelemetry-python-genai` repository (verified 2026-08-24; the
old package now receives only security patches). The rename did not change
the wire scope: the renamed package still emits through
`opentelemetry.util.genai.handler`, which the existing
`opentelemetry.util.genai` prefix already claims. The
`opentelemetry.instrumentation.genai` prefix pre-added for this rename
matched no shipping package as a result, so the edge no longer claims it;
it can return if upstream ever changes the emitted scope name.

Research reflects the Cursor wire reference as of 2026-08-21.

Cursor (Anysphere) exports native OTel as an Enterprise-plan beta configured
server-side in Team Settings: OTLP/HTTP to `<base>/v1/metrics` and
`<base>/v1/logs`, instrumentation scope `cursor.telemetry`/`0.1.0`,
metrics and logs only — no distributed traces. Log records carry the
correlation state: `cursor.event.id` (always; the dedupe key, deterministic
across retries and Kafka replay), optional `cursor.conversation.id`
(composer UUID or `bc-...` cloud-agent id), and optional
`cursor.usage_event.id` (request-grain join key). Event bodies carry no
prefix (`api_request`, `api_error`, `api_correction_<kind>`,
`skill_activated`, `hook_execution_complete`, `plugin_installed`,
`cloud_agent_*`). Delivery is at-least-once with no ordering guarantee, so
corrections can arrive after the requests they amend. No prompt, completion,
or tool content exists anywhere on this wire; tool calls appear only as
metric datapoints without correlation IDs. See the
[Cursor OpenTelemetry Export wire reference](https://cursor.com/docs/enterprise/opentelemetry-export/wire).

Relevant primary sources:

- [Codex advanced configuration](https://developers.openai.com/codex/config-advanced#observability-and-telemetry)
- [OpenAI Codex telemetry source](https://github.com/openai/codex/tree/main/codex-rs/otel)
- [Cursor OpenTelemetry Export](https://cursor.com/docs/enterprise/opentelemetry-export)
- [Cursor OpenTelemetry Export wire reference](https://cursor.com/docs/enterprise/opentelemetry-export/wire)
- [Claude Code monitoring and native span hierarchy](https://code.claude.com/docs/en/monitoring-usage#traces-beta)
- [Collector Builder](https://opentelemetry.io/docs/collector/extend/ocb/)
- [opentelemetry-instrumentation-openai-v2](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai/opentelemetry-instrumentation-openai-v2)
- [opentelemetry-util-genai handler](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/util/opentelemetry-util-genai)
- [Strands Agents traces documentation](https://strandsagents.com/docs/user-guide/observability-evaluation/traces/)
- [Strands tracer source](https://github.com/strands-agents/sdk-python)

## Component shape

One Collector component type, `coding_agent`, exposes two edges:

- logs to traces: stateful correlation behind a claiming router — Codex
  per-turn synthesis and Cursor burst synthesis;
- traces to traces: stateless native-trace normalization (Claude Code,
  OpenCode, and GenAI semconv sources) behind a claiming router.

Keeping these under one component makes the distribution and provider contract
easy to discover while retaining signal-correct Collector behavior. The input
pipelines export raw telemetry in parallel before normalization.

### Package architecture

The connector is its own Go module at `connector/codingagentconnector/`, matching
the per-component module layout of `opentelemetry-collector-contrib` and
upstreams with minimal changes. The E2E validator and the tooling/OCB modules
(`internal/tools`, generated `dist`) are separate modules built independently;
the repository deliberately avoids a `go.work` workspace so each module builds in
isolation, as in Contrib.

The component module root contains `Config`, `NewFactory`, the factory edge
adapters, and the mdatagen inputs/outputs (`metadata.yaml`, `doc.go`, and the
mdatagen-generated files). Stateful Codex logic lives in `internal/codex`;
Cursor burst logic lives in `internal/cursor`; native
Claude logic lives in `internal/claude`; GenAI normalizer logic lives in
`internal/genai`; OpenCode normalizer logic lives in `internal/opencode`;
`internal/metadata` has mdatagen-generated code.
This prevents provider schemas and correlation helpers from becoming accidental
public API.

This organization drew from the Contrib count, routing,
spanmetrics, and servicegraph connectors current when this connector was
first built. The implementation follows their
common patterns: a small `NewFactory` with type and per-signal stability sourced
from the generated `metadata` package, typed default configuration, Collector
`Validate`, factory/config conformance and generated lifecycle tests, `goleak`
leak checks, downstream consumers injected at construction, explicit
`Start`/`Shutdown`, copy-on-write when reporting `MutatesData: false`, bounded
state, self-observability metrics via the mdatagen `TelemetryBuilder`, and
background loops owned by component lifecycle. Mature single-purpose Contrib
connectors generally keep behavior in one package and reserve `internal` for
metadata or stores. This component has two intentionally different provider
paths, so provider-focused internal packages give the same encapsulation without
mixing stateful and stateless logic.
The component registers standard logs and traces edges, so it uses
`connector.NewFactory`; it does not depend directly on the profile-aware
experimental `xconnector` factory.

## Canonical semantics

The normalized tree uses released GenAI vocabulary where applicable:

```text
invoke_agent <agent>
├── chat <model>
└── execute_tool <tool>
```

Common attributes include:

- `gen_ai.operation.name`
- `gen_ai.agent.name`
- `gen_ai.provider.name`
- `gen_ai.request.model`
- `gen_ai.conversation.id`
- `gen_ai.tool.name`
- `gen_ai.usage.input_tokens`
- `gen_ai.usage.output_tokens`
- `coding_agent.client.name`
- `coding_agent.client.version`
- `coding_agent.source.event`
- `coding_agent.source`

The complete emitted vocabulary is a closed list, tracked in
[docs/canonical-attributes.md](canonical-attributes.md) alongside each
harness's raw → canonical mapping matrix under
[docs/harnesses/](harnesses/). Every edge remaps all attributes explicitly
into that vocabulary; prefix pass-through is not permitted, and a
cross-harness conformance test fails CI on drift.

Custom attributes remain under `coding_agent.*` and migrate as the
semantic conventions evolve.

`gen_ai.provider.name` describes the API the agent speaks, not the operator that
served the request: it reads `openai` for Codex and `anthropic` for Claude Code even
when either targets a third-party endpoint. Neither agent logs the upstream
host, so a proxied setup does not distinguish from a direct one.

Codex's wire carries a `provider_name` display label on
`codex.conversation_starts`, but the label holds operator-authored text, not
an identifier, and appears only once per session — so the connector drops it
rather than mapping it anywhere (see [docs/harnesses/codex.md](harnesses/codex.md)).
Each edge sets `gen_ai.provider.name` itself from what the source reliably
reports (`openai` for Codex; nothing for edges whose wire never names a
provider).

## Codex correlation model

### Key and turn boundary

State keys on provider plus `conversation.id`. Codex reuses a conversation
ID across turns, so each `codex.user_prompt` begins a new turn. If non-prompt
events arrive first, the normalizer creates an orphan state and the later prompt can fill
it during the reorder window.

A turn may contain many model calls separated by tools. The
first `response.completed` cannot close a turn. After any completion event, the
turn closes only when no further event arrives for `reorder_window`. A later
tool result or model request invalidates the earlier completion and requires a
new `response.completed`; every later event resets the quiet period.

Other finalization reasons are:

- `superseded`: another prompt arrives for the conversation;
- `timeout`: no completion arrives before `turn_timeout`;
- `shutdown`: the Collector drains active state;
- `evicted`: the active-turn bound admits a newer conversation.

The emitted root carries no turn-state attributes: the close reason survives
only as the timeout span status and the `otelcol_coding_agent_turns_emitted`
metric's `finish_reason` label (see [docs/harnesses/codex.md](harnesses/codex.md)).
Timeout roots use OTel error status. Shutdown and superseded roots remain unset
because they do not necessarily represent an agent failure.

### Reordering

Records in each incoming pdata batch sort by event timestamp. Cross-batch
late arrivals have acceptance until the wall-clock reorder window expires. The
wall clock drives finalization because source timestamps may skew or
batch; source timestamps still drive span timing.

### Span construction

- Root start/end cover all observed event timestamps and duration-derived starts.
- Each model call becomes one `chat` span, built from the turn-completion
  `response.completed` record; the timing-only duplicate skips. A call whose
  provider reported no usage still gets its span, just without `gen_ai.usage.*`.
- The most recent preceding API request supplies the model-call start when present.
- Each `codex.tool_result` becomes an `execute_tool` span; a failed result
  (`success=false`) sets the span's OTel error status without carrying a
  decision attribute.
- The builder drops `codex.tool_decision` records from canonical output.
- Other safe operational events become root span events (names and
  timestamps only).
- Usage lives on chat spans only; the root carries no usage of its own.

Trace IDs derive from SHA-256 of provider, conversation ID, and prompt
timestamp. Span IDs add a stable role/event discriminator. This makes replay of
the same complete event set idempotent. It does not deduplicate exports by
itself, and an orphan turn without a prompt may derive a different ID if its
earliest observed event changes across replay boundaries.

Within a live turn, redelivered events deduplicate by a content
fingerprint (event name, timestamp, and retained attributes). OTLP delivery is
at-least-once, so without this a resent batch would double-count token usage,
duplicate `chat`/`execute_tool` spans, and let a redelivered prompt falsely
supersede its own turn. The fingerprint set bounds at `max_events_per_turn`.

### Bounds

`max_active_turns` bounds concurrent correlation state. The least recently
observed turn emits before admitting a new one. `max_events_per_turn`
limits individual turn memory; truncation surfaces through the
`otelcol_coding_agent_turns_truncated` metric rather than a span attribute.
These controls are intentionally configuration-driven.

## Cursor correlation model

### Key and burst boundary

State keys on `cursor.conversation.id` alone (composer UUIDs on IDE/CLI,
`bc-...` ids for cloud agents; unique in practice). The wire has no
user-prompt or turn-boundary event, so no closer approximation of a user turn
exists: a "turn" is an activity burst. Each record for a conversation with no
active burst opens one, and a conversation resumed after finalization opens a
new burst that emits a new trace segment carrying the same
`gen_ai.conversation.id`.

Finalization reasons:

- `quiet` — no new record for `reorder_window`. The normal close; unlike
  Codex, quiet closes unconditionally because the wire has no completion
  event to require first.
- `timeout` — the burst has been open longer than `turn_timeout`, measured
  from its first event. A burst that keeps receiving records never goes
  quiet, so this is the only cap on burst growth; error status, mirroring
  Codex timeouts.
- `shutdown` — Collector drain.
- `evicted` — LRU eviction past `max_active_turns`.

### Dedupe and replay

Within a live burst, redelivered records dedupe exactly on `cursor.event.id`,
the wire's own dedupe key — simpler and stronger than the Codex content
fingerprint. The id set bounds at `max_events_per_turn` with the rest of the
burst state.

Trace IDs derive from SHA-256 of the provider marker `cursor`, the
conversation id, and the burst's first `cursor.event.id`; span IDs add a
stable role/event discriminator. Because `cursor.event.id` is deterministic
across retries and Kafka replay, a full replay of the same burst derives
identical IDs and merges idempotently downstream. It does not deduplicate
exports by itself: a partial replay arriving after finalization can still
emit a fragment trace with a different ID, the same documented Codex
limitation. Cross-burst dedupe state stays out of scope; consumers needing
replay-proof reads dedupe on trace ID.

### Timestamps and ordering

Records sort by record timestamp before feeding state (the wire guarantees no
ordering). Timestamp resolution: record timestamp, else observed timestamp,
else wall clock, matching Codex; the internal inferred-timestamp marker
never reaches emitted spans.

### Bounds

`max_active_turns` bounds concurrent bursts with LRU eviction;
`max_events_per_turn` bounds per-burst memory; truncation surfaces through
the `otelcol_coding_agent_turns_truncated` metric, identical to Codex.

### Span construction

Emitted tree per burst:

```text
invoke_agent cursor
├── chat <model>        (one point span per cursor.api_request)
└── [no execute_tool — the native wire cannot express them]
```

Root `invoke_agent cursor`: start = first event timestamp, end = last event
timestamp. It carries `gen_ai.operation.name=invoke_agent`,
`gen_ai.agent.name=cursor`, `gen_ai.conversation.id`,
`coding_agent.client.name=cursor`, `coding_agent.client.version` from
resource `service.version` when present (desktop/CLI clients only; cloud
agents carry none), and `coding_agent.source=normalized`. Cursor's
vendor detail — resource-side surface/entrypoint/team/user attributes,
billable flags, correction kinds, skill/hook/cloud-agent payloads — stays
out of canonical output; the full raw → canonical matrix lives in
[docs/harnesses/cursor.md](harnesses/cursor.md).

The root never sets `gen_ai.provider.name`: the wire never names the upstream
model provider and the connector does not guess one. It carries no
completion marker either: quiet closing cannot distinguish a finished model
turn from an abandoned one. `timeout` sets error status; shutdown and evicted
leave status unset, mirroring Codex.

Each `api_request` record becomes one chat span named `chat <model>` (bare
`chat` when the model attribute is absent). Point spans: the wire reports
tokens at request grain with no timing, and the connector does not invent
durations. Chat spans carry `gen_ai.request.model` and the four token counts.

In-burst `cursor.usage_event.id` joins: an `api_error` sharing a request's
usage-event id sets that chat span's status to Error; an `api_correction`
attaching to the same id becomes an event on that span. When the counterpart
arrived in an earlier, already-finalized burst — expected, since corrections
trail their requests — the event lands on the root instead. The join key
serves only internal correlation and does not survive onto emitted spans or
events.

Root span events hold every non-chat record the chat spans did not consume:
unjoined `api_error` and `api_correction` records, `skill_activated`,
`hook_execution_complete`, `cloud_agent_*` lifecycle records, and unknown
event bodies. Events carry names and timestamps only — a correction kind
stays readable in the event name itself (`api_correction_<kind>`) — with no
vendor attributes.

Attribute policy is an allowlist: the builder copies only the fields named
above from records into spans; everything else stays in the raw pipeline.
This keeps canonical output content-free even if Cursor later adds fields,
per the repo's allowlist-over-denylist rule and the closed canonical
vocabulary in [docs/canonical-attributes.md](canonical-attributes.md).

## Claude Code normalization

The normalizer copies the input batch before modification because the Collector may fan it
out to other consumers. The normalizer changes only these native span types:

| Native name | Canonical name | Canonical operation |
| --- | --- | --- |
| `claude_code.interaction` | `invoke_agent claude_code` | `invoke_agent` |
| `claude_code.llm_request` | `chat <model>` | `chat` |
| `claude_code.tool` | `execute_tool <tool>` | `execute_tool` |

All other Claude spans, including permission wait, tool execution, hooks, and
subagent descendants, keep their native names and hierarchy. The normalizer
adds provider/client/source attributes and maps `session.id` to
`gen_ai.conversation.id` on the interaction span when available.
Resource groups without any Claude Code span are not emitted by this edge; they
remain available in the parallel raw trace pipeline without polluting the
canonical coding-agent pipeline.

## GenAI semconv normalization

A stateless package `internal/genai` joins `internal/codex` and
`internal/claude`. The traces-to-traces edge assigns each resource-spans group
to at most one normalizer:

1. Group contains a `claude_code.`-prefixed span: Claude normalizer,
   unchanged behavior.
2. Otherwise, group contains a scope-spans block whose instrumentation-scope
   name matches the GenAI allowlist: GenAI normalizer.
3. Otherwise the group is not part of the canonical edge output. It remains
   available in the parallel raw pipeline.

Claude-first claiming means a resource group never emits twice and
existing Claude deployments see no behavior change. Like the Claude edge,
the GenAI normalizer copies the whole claimed resource group, including
spans from the application's own tracer scopes, so an ad-hoc agent's manual
parent spans keep their children and trace parentage stays intact.

### Detection

Scope-name matching, evaluated per scope-spans block:

| Rule | Matches |
| --- | --- |
| `opentelemetry.instrumentation.openai_v2` (prefix) | openai-v2 default mode |
| `opentelemetry.util.genai` (prefix) | openai-v2 experimental mode and direct util-genai users |
| `strands.telemetry` (prefix) | Strands Agents SDK built-in tracer |

Within a claimed group, the normalizer rewrites a span only when its scope
matched and it carries `gen_ai.operation.name`. Everything else in the group
passes through untouched, mirroring how the Claude edge leaves
`claude_code.tool.execution` and hook spans alone.

### Name normalization

Span names conform to the canonical `{operation} {subject}` shape. The
normalizer rewrites the name only when the subject attribute is present;
otherwise it keeps the emitted name:

| `gen_ai.operation.name` | Canonical name | Subject attribute |
| --- | --- | --- |
| `chat` | `chat {model}` | `gen_ai.request.model` |
| `invoke_agent` | `invoke_agent {agent}` | `gen_ai.agent.name` |
| `execute_tool` | `execute_tool {tool}` | `gen_ai.tool.name` |

openai-v2 already emits `chat {model}`; Strands emits bare `chat` and gains
the suffix. Operations outside this table (`embeddings`,
`invoke_workflow`, `execute_event_loop_cycle`, `memory.*`, multiagent
operations) keep their emitted names and hierarchy.

### Attribute normalization

- `gen_ai.provider.name`: kept if present; otherwise the normalizer copies
  the value from legacy `gen_ai.system`, and it removes `gen_ai.system` from
  canonical output in both cases. Values stay as emitted (`openai`,
  `strands-agents`); the connector does not guess the upstream model
  provider, consistent with the existing `gen_ai.provider.name` stance.
  Strands names the framework, not the model provider; the connector
  preserves the value.
- Token usage: when `gen_ai.usage.input_tokens` is absent and
  `gen_ai.usage.prompt_tokens` is present, the normalizer copies the value
  (same for output/completion). It removes legacy
  `prompt_tokens`/`completion_tokens` keys from canonical output either
  way. `total_tokens` and cache read/write counts pass through unchanged.
- Provenance: `coding_agent.source=native`,
  `coding_agent.source.scope=<original instrumentation scope name>` (the
  GenAI analog of `coding_agent.source.event`),
  `coding_agent.client.name` from resource `service.name` and
  `coding_agent.client.version` from `service.version` when present. An
  ad-hoc agent's identity is its service, not a known client binary.
- Everything else passes through: `server.address`/`server.port`, request
  parameters, `gen_ai.response.*`, `gen_ai.conversation.id` when an agent
  sets it, span kind, status, IDs, and links.

### Content stripping

Applied to every span in a claimed group (content keys only appear on GenAI
spans, so this is a cheap blanket rule). The lists cover current emitters
plus known older Strands layouts.

Span attributes removed:

- `gen_ai.input.messages`, `gen_ai.output.messages`,
  `gen_ai.input.messages.ref`, `gen_ai.output.messages.ref`
- `gen_ai.system_instructions`, `system_prompt`
- `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result`
- `gen_ai.tool.definitions`, `gen_ai.agent.tools` (bulk tool schemas;
  potentially sensitive, available in raw)
- `gen_ai.user.message`, `gen_ai.assistant.message`,
  `gen_ai.system.message`, `gen_ai.tool.message`, `gen_ai.choice`,
  `gen_ai.choice.message`, `gen_ai.choice.tool.result` (older Strands
  attribute layouts and the `gen_ai_span_attributes_only` mode)

Span events removed entirely (with their attributes):

- `gen_ai.client.inference.operation.details`
- `gen_ai.user.message`, `gen_ai.assistant.message`,
  `gen_ai.system.message`, `gen_ai.tool.message`, `gen_ai.choice`
- `memory.query`, `memory.content`

openai-v2's default mode sends content to log events, which never enter this
edge, so stripping matters for Strands defaults and openai-v2 experimental
`span_only`/`span_and_event` capture modes.

### Decision record

- Stateless pass-through, exactly like the Claude edge: preserve IDs and
  hierarchy, never synthesize spans. Traces without an `invoke_agent` root
  stay rootless.
- The normalizer strips content-bearing span attributes and events from
  canonical output. Full fidelity remains in the parallel raw pipeline.
- Strands sets `gen_ai.provider.name=strands-agents` (framework, not model
  provider); the connector preserves the value as emitted.
- A resource group holding both a `claude_code.*` span and a GenAI-scope
  span goes wholly to the Claude edge, whose behavior does not change, so
  GenAI content keys on such a mixed group would not pass through the GenAI
  content stripper. No known agent emits that combination.

## OpenCode normalization

A third stateless package, `internal/opencode`, joins the traces edge. It
claims a resource group iff any instrumentation scope bears the exact name
`opencode`. The match is exact rather than prefixed on purpose: `opencode.*`
scopes belong to plugins and `com.opencode` to the Kilo fork — separate
surfaces this edge must not claim — and exact naming keeps its claims disjoint
from the Claude and GenAI rules by construction.

Inside a claimed group, three Vercel AI SDK span names rewrite in place and
everything else — the internal Effect instrumentation making up most of the
wire — stays out of canonical output. `ai.streamText`, one per model step,
becomes `invoke_agent opencode`, carrying `gen_ai.conversation.id` mapped from
`session.id` on the span (falling back to the resource) and the step's usage
totals: `ai.usage.inputTokens`/`outputTokens` map onto
`gen_ai.usage.input_tokens`/`output_tokens`, and `ai.usage.cachedInputTokens`
maps onto `gen_ai.usage.cache_read.input_tokens`, and
`ai.usage.reasoningTokens` (with its `outputTokenDetails` fallback) maps
onto `gen_ai.usage.reasoning.output_tokens`; other token-detail counters
stay out. Each
`ai.streamText.doStream` child becomes `chat <model>` from its own
`gen_ai.request.model` (bare `chat` when absent), and each `ai.toolCall` child
becomes `execute_tool <tool>` from `ai.toolCall.name`. Renamed spans are
rebuilt with only these attributes plus the common marker set used by the
other edges (`coding_agent.source=native`, `coding_agent.client.name`,
`coding_agent.client.version`, and `coding_agent.source.event` holding the
original wire name), so content such as `ai.response.text` and
`ai.toolCall.args`/`result` has no path into canonical output — removal is
structural allowlisting, not a denylist that fails open as the wire evolves.
Every claimed span copies a non-empty `ai.model.provider` onto
`gen_ai.provider.name`; the wire's `gen_ai.system` holds the SDK name, not a
provider, and drops.

Granularity is per step: every `ai.streamText` span becomes its own canonical
root inside one long-lived per-session trace — the wire keeps one TraceId per
session — so a session of N model steps yields N `invoke_agent opencode`
roots downstream. Canonical roots are also re-rooted, their parent span ID
cleared, because the wire nests each step under internal Effect spans
(`SessionProcessor.process` → `LLM.run`) that never reach canonical output;
keeping such a parent would leave it dangling in every backend. Like the
Claude edge, the normalizer stays stateless and rewrites whatever each export
contains: OpenCode fragments its output, children can land without their
ancestors, and backends reassemble by the preserved IDs.

## Pi normalization

The Pi coding agent has no native OTLP export; the
[`@amaster.ai/pi-telemetry`](https://www.npmjs.com/package/@amaster.ai/pi-telemetry)
extension (Apache-2.0) fills that gap. It emits OTLP/HTTP traces only — no
logs or metrics — scoped to user-input boundaries: one trace per user
message, containing every agentic iteration and tool call until the reply
goes out.

The edge claims groups by instrumentation-scope prefix
(`@amaster.ai/pi-telemetry`) or by the resource `telemetry.sdk.name`
attribute. A live capture on 2026-08-22 (extension 0.1.9, pi 0.84.2) pinned
the wire shape and ships as the golden fixture:

| Native span | Canonical name | Notes |
| --- | --- | --- |
| `chat-turn` (one per iteration) | `invoke_agent pi` | parent cleared |
| `llm-generation [lane] [n]` (prefix match) | `chat <model>` | usage keys renamed to canonical `gen_ai.usage.*`; cache keys use the dotted semconv form |
| `<tool name>` with a `toolName` attribute | `execute_tool <tool>` | tool identity lives in attributes (`toolName`, `toolCallId`); the span name is the bare tool name |

### Decision record

- Hierarchy does not survive export intact. Children reference the
  `chat-turn` span they ran under, but a batch can arrive without the turn it
  references, and the pinned capture shows successive iterations reusing one
  turn span ID (so their children all attach to its first occurrence). Each
  `chat-turn` becomes a root with any dangling parent cleared,
  matching the plan-level rule that turn grouping relies on
  `gen_ai.conversation.id` rather than a long-lived session root. Orphaned
  `chat`/`execute_tool` spans re-attach to the first agent root in their
  batch; children arriving in a batch of their own become roots, mirroring
  the Claude Code sub-batch behavior.
- Exporter-local metadata never reaches canonical output: the edge drops
  `langfuse.*` attribute baggage, the serialized `usage` JSON object
  (renaming the flat per-field source keys instead), cost totals, and
  diagnostic `status` / `stopReason` fields. Payload capture stays off by
  default in the extension (`includePayloads`).
- Unknown span names match no native shape, so they drop from canonical
  output along with non-native sibling spans; the raw pipelines preserve
  them, and a future extension event needs a new mapping here to reach
  canonical output.
- The extension's own telemetry contract differs from Pi's first-party
  `@earendil-works/pi-telemetry` package, which defines vocabulary but ships
  no exporter. Supporting the third-party extension here reflects what
  actually exists on the wire today; if Pi grows a first-party exporter, it
  becomes another claim profile in this edge.

## OpenHands correlation model

The OpenHands SDK instruments through the Laminar tracer, so its native
traces arrive under the scope `lmnr.tracer`. Research reflects primary
sources as of 2026-08-23 (SDK commit `9421149`, lmnr 0.7.56); the committed
fixtures pin that wire.

### Claiming

Every Laminar-instrumented application shares the `lmnr.tracer` scope, so
scope matching alone would claim unrelated applications. A resource group
belongs to this edge only when an `lmnr.tracer` span carries an explicit
OpenHands marker: one of the SDK's conversation- and step-family span names
(`conversation`, `conversation.send_message`, `conversation.run`,
`agent.step`, and siblings) or the delegate metadata flag. The router
claims Claude-first like every other edge, so a group never emits twice.

### Canonical mapping

Spans classify by the Laminar span type: `LLM` becomes `chat <model>`,
`TOOL` becomes `execute_tool <tool>`, and the long-lived `conversation`
span becomes the `invoke_agent openhands` root. Every other span in the
group drops from canonical output, but still widens the root's time
envelope for its trace ID. Kept children reparent under the root; a tool
call and its result share a `tool_call_id` and dedupe to one span. The
root maps `gen_ai.conversation.id` from
`lmnr.association.properties.session_id`; `user_id` and conversation tags
are vendor detail outside the vocabulary and stay in the raw pipeline.

The normalizer stays stateless like the OpenCode edge: mid-conversation
exports can arrive before their `conversation` root ends, so such fragments
get a synthetic root whose span ID derives from SHA-256 of the trace ID.
Delegates run their own sessions, so they arrive as sibling traces sharing
the conversation id rather than as descendants. The delegate metadata flag
serves claiming only, and the linkage detail (task id, subagent type,
parent session id, tool call id) stays in the raw pipeline; downstream
consumers reconstruct delegation through the shared conversation id.

### Usage and content

Chat spans remap the LiteLLM accounting keys onto `gen_ai.usage.*` —
input, output, total, and cache read/creation input tokens — each key only
when the wire carries it. Streamed completions carry no token usage
upstream, so a chat span may carry no usage at all. The wire reports no
cost and no reasoning-token counts on spans.

Attribute policy is a structural allowlist like the OpenCode edge: renamed
spans rebuild with only the mapped attributes plus the common marker set,
so the content-heavy Laminar wire — prompt and completion payloads ride
span attributes by default — has no path into canonical output. Full
fidelity remains in the parallel raw pipeline.

## Privacy and security

The connector discards Codex prompt content, tool arguments, and tool output
from its in-memory event copy and never copies them into synthetic spans.
Safe length/count/status fields stay. Raw telemetry is
still exported by the example pipeline, so operators must apply their own
retention, authorization, and redaction policies to that raw destination.

Unlike Codex and Claude Code, where content requires enabling explicit
gates, Strands exports prompt and completion content by default and its
redaction is opt-in. Under default agent settings the raw traces destination
receives content. Configure Strands redaction
(`gen_ai_unredacted_attributes` token) or apply raw-pipeline access policy
accordingly. Canonical output strips content-bearing attributes and events
regardless.

Recommended endpoint defaults:

- leave Codex `log_user_prompt=false`;
- leave Claude `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS`,
  `OTEL_LOG_TOOL_CONTENT`, and `OTEL_LOG_RAW_API_BODIES` disabled;
- authenticate and encrypt OTLP outside the local Compose test;
- filter user identity attributes if they are not required.

## Restart and delivery behavior

State is in memory. Collector shutdown drains incomplete turns, but a crash can
lose active state. Persistent state defers until restart continuity is a
demonstrated need; adding it would require an explicit storage contract,
schema versioning, and replay/deduplication policy.

The connector returns synchronous downstream errors during ingestion-triggered
flushes. Background finalization logs downstream failures because the original
receiver request is no longer active. Production deployments should use a
reliable downstream exporter with queue/retry support.

The S3 reference configuration fans out ingestion so normalization never
replaces source data. It uses one exporter for each of raw Codex logs, raw
Claude/native traces, and canonical normalized traces. Separate exporter
instances give each stream an independent S3 prefix and persistent queue
identity. Their `sending_queue` instances use the file-storage extension with
`fsync` enabled, bounded request counts, bounded database size, and indefinite
retry. This preserves completed batches across Collector restarts while bounding
local disk use. Once either bound exhausts, backpressure or data loss remains
possible, so production capacity and alerting must size for the expected S3
outage.

File storage is deliberately local rather than shared. Each Collector replica
must own a durable volume, and replacing a replica without its volume abandons
that replica's queue. AWS credentials resolve externally through the SDK
credential chain; static secrets do not belong in the Collector configuration.
Persistent exporter queues do not change the connector state contract above:
a crash can still lose an active Codex turn.

## Testing strategy

The ordinary, non-billable suite covers parsing, validation, deterministic IDs,
canonical trees, redaction, token mappings, status, out-of-order batches,
multi-turn splitting, bounds, shutdown drain, factory creation, Claude
copy-on-normalize behavior, and GenAI semconv normalization. Race testing
exercises the timer/consumer boundary.

The live Compose E2E is intentionally opt-in because it incurs a real model
request. It:

1. builds the pinned OCB distribution;
2. starts an OTLP receiver with raw and canonical file exporters;
3. runs pinned Codex in a container with a unique resource run ID;
4. requires one harmless shell tool invocation;
5. waits for the quiet-window finalizer and batch exporter;
6. parses actual OTLP JSON with Collector pdata;
7. checks the canonical root, chat/tool children, trace parenting, completion,
   conversation ID, and sensitive-attribute absence.

The agent process has a configurable ten-minute default timeout so retries or
transport stalls cannot leave an unbounded paid session running.

Both E2Es run against z.ai's GLM models, so one z.ai API key covers both stacks.
Nothing in the connector is provider-specific; z.ai is simply what these tests
connect to. The Codex runner pins `glm-4.7`, the model the connector's pinned
regression captures came from. Its image installs Debian's standard
`ca-certificates` package so public TLS works without host paths. No
`codex login` step exists: the model provider reads its key from `env_key`
directly.

Codex (since 0.144.1) speaks only the Responses API, which z.ai does not serve, so a third
Compose service (`responses-proxy`) translates Responses to Chat Completions and
injects `stream_options.include_usage` so token usage survives the stream.
The proxy builds from a pinned fork commit, exists only for this test, and is not
part of the connector or any production path. The proxy is the only container
that receives the real z.ai key; Codex itself gets a placeholder, because its
`env_key` variable needs setting but the value only becomes a bearer token the
proxy ignores. Codex's inner bubblewrap sandbox cannot create a user namespace
inside an unprivileged container, so the container disables sandboxing and acts as
the isolation boundary: no host mount, no real credential. Model-issued commands do
get writes and egress inside that container.

Normal verification prepares and compiles the E2E, but the automated test command
does not invoke it.

The Claude Code E2E uses its own Compose file (`compose.e2e-claude.yaml`) that
defines only the Claude agent, so it cannot accidentally require or consume the
Codex credential. It reaches z.ai's Anthropic-compatible endpoint via
`ANTHROPIC_BASE_URL`, and the container receives exactly one credential:
`ANTHROPIC_AUTH_TOKEN`. `ANTHROPIC_API_KEY` is never set, so it cannot shadow the
auth token, and the Compose `environment:` block remains an allowlist of what
reaches the container. GLM models map onto Claude Code's model tiers. It
runs the current pinned Claude Code release in bare print mode with only Bash
exposed, explicit tool approval, bounded turns, a hard dollar ceiling, and no
session persistence. Claude exports only beta traces; content-bearing telemetry
gates remain disabled. Validation requires both the untouched native hierarchy and
its normalized counterpart, including the interaction, LLM request, and Bash tool
spans.

The openai-adhoc E2E uses a pinned Python image with pinned `openai`,
`opentelemetry-instrumentation-openai-v2`, SDK, and OTLP exporter packages. A
small ad-hoc agent script makes one chat-completions call to z.ai's
OpenAI-compatible endpoint (no responses-proxy needed, unlike Codex) and
force-flushes. The script runs twice in one container run, once per convention
mode via `OTEL_SEMCONV_STABILITY_OPT_IN`, under distinct service names, so
one paid run validates both modes. Validation: normalized rootless `chat`
spans for both modes, provider `openai`, usage tokens, no content keys.

The Strands E2E uses a pinned `strands-agents` image with its
OpenAI-compatible model provider pointed at z.ai and one harmless tool the
prompt forces. Validation: untouched native tree in raw output, normalized
`invoke_agent`/`chat`/`execute_tool` in canonical output, content present in
raw and absent in canonical.

Because one logical trace arrives as many OTLP exports — an agent flushes each
span as it ends, so the interaction root lands after the children it parents — the
validator merges every batch in the output file before asserting, and reassembles
spans the way a trace backend would. A run can also contain more than one span
named like the root, since the Codex connector emits a root per turn and a turn
finalized by timeout, eviction or supersession is incomplete by design; validation
so tries every candidate root and fails only when none of them validates.

CI never runs any paid E2E. It compiles their runners and validator, builds
every image including the responses-proxy, validates both Compose graphs and
Collector configurations, asserts the credential split described above, and runs
normal plus race-enabled tests. Tags release by GoReleaser after OCB
generates the custom Collector main package; release output stays separate
from OCB's generated `dist` source tree.

The Collector resource processor adds the unique run marker before
correlation. Codex constructs a fixed SDK resource and does not currently honor
`OTEL_RESOURCE_ATTRIBUTES`, so relying on the agent container environment would
make stale-output detection ineffective.

## Known limitations and future work

- Codex state does not persist across crashes.
- Collector replicas require consistent routing by conversation ID or
  shared state (see [multi-instance-ha.md](multi-instance-ha.md)).
- The connector intentionally ignores coding-agent logs without a conversation ID.
- Implemented sources:
  - Codex log synthesis.
  - Cursor log synthesis.
  - Claude Code native-span normalization.
  - GenAI semconv normalization (openai-v2, util-genai, Strands).
  - OpenCode native-span normalization (Vercel AI SDK spans).
  - GitHub Copilot native-span normalization (via the GenAI edge, scope
    prefix `github.copilot`).
  - OpenHands SDK native-trace normalization (Laminar instrumentation).
- Opt-in root synthesis for rootless ad-hoc traces (explicitly deferred; a
  config flag behind which the connector synthesizes `invoke_agent` parents is
  future work if rootless traces prove common).
- Configurable scope allowlist extension.
- Upstream scope names are pre-1.0. The announced package rename landed
  without a scope change (the renamed genai-openai package kept the
  util-genai scope); rerun fixtures and E2Es before bumping pins.
- Cursor: implemented as native-log synthesis with fixture-based validation.
  A live Cursor E2E stays blocked on Enterprise-only server-side configuration
  (no Enterprise access available); tool-call children would need Cursor to
  log tool calls with a conversation id — today they are metrics without
  correlation IDs. The wire surface was re-verified against the 2026-08-22
  reference: the edge covers all ten log events, `cloud_agent.mcp_auth_error`
  maps its server attribute onto the root event, and correction records
  annotate the joined chat span with the correction kind instead of dropping
  its token totals (deliberate; downstream decides billing semantics).
- The GenAI edge handles Copilot native traces. A live Copilot E2E
  stack runs the CLI against a BYOK provider (no GitHub auth or subscription
  needed): provider type/base URL/key/model arrive via `COPILOT_PROVIDER_*`
  environment variables. A renamed producer scope (`COPILOT_OTEL_SOURCE_NAME`)
  does not claim.
- Committed OTLP fixtures from real Codex, Claude, and GenAI runs (sanitized)
  run through `e2e/validator` as fast, unpaid unit tests: every harness with a
  committed canonical fixture (codex, claude, cursor, copilot, openhands,
  strands, openai-adhoc) is one table case there. The paid live E2Es remain the
  check that captures stay fresh.
- Provider schemas are not stable APIs; fixtures and E2E should be rerun before
  upgrading pinned client or Collector versions.
- Upstream semantic-convention changes may replace some `coding_agent.*` fields.
