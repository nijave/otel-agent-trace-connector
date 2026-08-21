# Design and assumptions

This document records the connector's current design and updates with the
implementation rather than serving as a future proposal.

## Goals

1. Produce a comparable trace per user turn across Codex and Claude Code.
2. Keep vendor telemetry in a parallel raw pipeline.
3. Keep provider mappings explicit and testable.
4. Bound memory and latency under missing, duplicated, delayed, or malformed events.
5. Avoid collecting prompt or tool content by default.
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
  `gen_ai.provider.name=openai`, adds Responses API coverage
  (`openai.api.type`), and can place content on span attributes
  (`gen_ai.input.messages`, `gen_ai.output.messages`) when capture is
  enabled. `opentelemetry-util-genai` also exposes inference, embedding,
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

Upstream is renaming the package to
`opentelemetry-instrumentation-genai-openai` in the new
`opentelemetry-python-genai` repository, which will change its scope name.

Relevant primary sources:

- [Codex advanced configuration](https://developers.openai.com/codex/config-advanced#observability-and-telemetry)
- [OpenAI Codex telemetry source](https://github.com/openai/codex/tree/main/codex-rs/otel)
- [Claude Code monitoring and native span hierarchy](https://code.claude.com/docs/en/monitoring-usage#traces-beta)
- [Collector Builder](https://opentelemetry.io/docs/collector/extend/ocb/)
- [opentelemetry-instrumentation-openai-v2](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai/opentelemetry-instrumentation-openai-v2)
- [opentelemetry-util-genai handler](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/util/opentelemetry-util-genai)
- [Strands Agents traces documentation](https://strandsagents.com/docs/user-guide/observability-evaluation/traces/)
- [Strands tracer source](https://github.com/strands-agents/sdk-python)

## Component shape

One Collector component type, `coding_agent`, exposes two edges:

- logs to traces: stateful Codex correlation;
- traces to traces: stateless native-trace normalization (Claude Code and GenAI
  semconv sources) behind a claiming router.

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
mdatagen-generated files). Stateful Codex logic lives in `internal/codex`; native
Claude logic lives in `internal/claude`; GenAI normalizer logic lives in
`internal/genai`; `internal/metadata` has mdatagen-generated code.
This prevents provider schemas and correlation helpers from becoming accidental
public API.

This organization drew from the v0.156.0 Contrib count, routing,
spanmetrics, and servicegraph connectors. The implementation follows their
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
- `coding_agent.model_provider`
- `telemetry.source`

Custom attributes remain under `coding_agent.*` and migrate as the
semantic conventions evolve.

`gen_ai.provider.name` describes the API the agent speaks, not the operator that
served the request: it reads `openai` for Codex and `anthropic` for Claude Code even
when either targets a third-party endpoint. Neither agent logs the upstream
host, so a proxied setup does not distinguish from a direct one.

What Codex does report is `provider_name` on `codex.conversation_starts`, copied to
`coding_agent.model_provider`. Two limits are worth knowing before relying on it.
The value is a display label authored by whoever wrote the provider block in
`config.toml` — Codex's own default reads `OpenAI` — and is not an identifier and
deliberately does not overwrite `gen_ai.provider.name`, whose consumers expect a
known value. Codex emits `conversation_starts` once per session, so only a
session's first turn carries it; later turns omit the attribute rather than
inheriting a value, which would require per-conversation state the connector does
not keep.

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

The emitted root records the finish reason and whether the turn is complete.
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
- Each `codex.tool_result` becomes an `execute_tool` span.
- Matching `codex.tool_decision` records become events on the tool span.
- Other safe operational events become root span events.
- Completion token counts sum across model calls for total root usage.

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
limits individual turn memory; excess events set
`coding_agent.turn.events_truncated=true`. These controls are intentionally
configuration-driven.

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
| `opentelemetry.instrumentation.genai` (prefix) | the announced upstream package rename |
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
- Provenance: `telemetry.source=native`,
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
an active Codex turn that can still lose in a crash.

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

Codex 0.144.1 speaks only the Responses API, which z.ai does not serve, so a third
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
  shared state.
- The connector intentionally ignores coding-agent logs without a conversation ID.
- Implemented sources:
  - Codex log synthesis.
  - Claude Code native-span normalization.
  - GenAI semconv normalization (openai-v2, util-genai, Strands).
- Opt-in root synthesis for rootless ad-hoc traces (explicitly deferred; a
  config flag behind which the connector synthesizes `invoke_agent` parents is
  future work if rootless traces prove common).
- Configurable scope allowlist extension.
- Upstream scope names are pre-1.0 and will change with the announced package
  rename; prefix matching mitigates, and rerun fixtures and E2Es before
  bumping pins.
- Add Cursor support: a provider edge that normalizes Cursor's telemetry into the
  canonical `invoke_agent`/`chat`/`execute_tool` vocabulary, plus a live E2E that
  exercises a real Cursor session and validates the exported OTLP. Blocked on
  confirming Cursor's telemetry format (logs vs. native traces, IDs, token/usage
  fields).
- Add GitHub Copilot support: the same provider edge + live E2E for Copilot,
  likewise pending confirmation of its telemetry format.
- Generate committed OTLP fixtures from real Codex, Claude, and GenAI runs
  (sanitized) so trace building and validation can exercise against real data
  as fast, unpaid unit tests, reducing reliance on the paid live E2E. The e2e
  validation logic already lives in plain Go tests (`e2e/validator`), so fixtures
  would slot in as table cases there.
- Provider schemas are not stable APIs; fixtures and E2E should be rerun before
  upgrading pinned client or Collector versions.
- Upstream semantic-convention changes may replace some `coding_agent.*` fields.
