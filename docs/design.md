# Design and assumptions

This document records the connector's current design. It is updated with the
implementation rather than treated as a future proposal.

## Goals

1. Produce a comparable trace per user turn across Codex and Claude Code.
2. Retain vendor telemetry in a parallel raw pipeline.
3. Keep provider mappings explicit and testable.
4. Bound memory and latency under missing, duplicated, delayed, or malformed events.
5. Avoid collecting prompt or tool content by default.
6. Package the component independently and compose it into a pinned Collector
   distribution with OCB.

The connector does not fork `opentelemetry-collector-contrib`, and it does not
expect runtime plugin loading. Component changes require a new OCB build.

## Research basis

Research was refreshed on 2026-07-11 against primary documentation and source.

Codex officially exports structured OTLP logs for conversation start, API,
SSE/WebSocket, user prompt, tool-decision, and tool-result activity. Shared
metadata includes `conversation.id`, model, client version, and timestamps.
`codex.sse_event` with `event.kind=response.completed` contains token counts.
The source also confirms tool correlation through `call_id` and durations in
milliseconds. User prompts, tool arguments, and outputs are content-bearing and
are deliberately excluded from generated spans.

Claude Code's beta trace exporter already creates one
`claude_code.interaction` root per prompt, with `claude_code.llm_request` and
`claude_code.tool` descendants. Rebuilding that hierarchy from its logs would
discard native trace context, span links, subagent nesting, and tool-execution
detail. The traces-to-traces path therefore copies the pdata batch, preserves
all IDs and hierarchy, and adds canonical naming and attributes.

Relevant primary sources:

- [Codex advanced configuration](https://developers.openai.com/codex/config-advanced#observability-and-telemetry)
- [OpenAI Codex telemetry source](https://github.com/openai/codex/tree/main/codex-rs/otel)
- [Claude Code monitoring and native span hierarchy](https://code.claude.com/docs/en/monitoring-usage#traces-beta)
- [Collector Builder](https://opentelemetry.io/docs/collector/extend/ocb/)

## Component shape

One Collector component type, `coding_agent`, exposes two edges:

- logs to traces: stateful Codex correlation;
- traces to traces: stateless Claude Code normalization.

Keeping these under one component makes the distribution and provider contract
easy to discover while retaining signal-correct Collector behavior. The input
pipelines export raw telemetry in parallel before normalization.

## Canonical semantics

The normalized tree uses released GenAI vocabulary where it is applicable:

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
- `telemetry.source`

Custom attributes remain under `coding_agent.*` so they can be migrated as the
semantic conventions evolve.

## Codex correlation model

### Key and turn boundary

State is keyed by provider plus `conversation.id`. Codex reuses a conversation
ID across turns, so each `codex.user_prompt` begins a new turn. If non-prompt
events arrive first, an orphan state is created and the later prompt can fill
it during the reorder window.

A turn may contain multiple model calls separated by tools. Consequently the
first `response.completed` cannot close a turn. After any completion event, the
turn closes only when no further event arrives for `reorder_window`. A later
tool result or model completion resets the quiet period.

Other finalization reasons are:

- `superseded`: another prompt arrives for the conversation;
- `timeout`: no completion arrives before `turn_timeout`;
- `shutdown`: the Collector drains active state;
- `superseded` on state eviction (currently shared with prompt supersession).

The emitted root records the finish reason and whether the turn is complete.
Timeout roots use OTel error status. Shutdown and superseded roots remain unset
because they do not necessarily represent an agent failure.

### Reordering

Records in each incoming pdata batch are sorted by event timestamp. Cross-batch
late arrivals are accepted until the wall-clock reorder window expires. The
wall clock is used for finalization because source timestamps may be skewed or
batched; source timestamps are still used for span timing.

### Span construction

- Root start/end cover all observed event timestamps and duration-derived starts.
- Each `response.completed` becomes a `chat` span.
- The most recent preceding API request supplies the model-call start when present.
- Each `codex.tool_result` becomes an `execute_tool` span.
- Matching `codex.tool_decision` records become events on the tool span.
- Other safe operational events become root span events.
- The last completion supplies aggregate root token usage.

Trace IDs are SHA-256-derived from provider, conversation ID, and prompt
timestamp. Span IDs add a stable role/event discriminator. This makes replay of
the same complete event set idempotent. It does not deduplicate exports by
itself, and an orphan turn without a prompt may derive a different ID if its
earliest observed event changes across replay boundaries.

### Bounds

`max_active_turns` bounds concurrent correlation state. The least recently
observed turn is emitted before admitting a new one. `max_events_per_turn`
bounds individual turn memory; excess events set
`coding_agent.turn.events_truncated=true`. These controls are intentionally
configuration-driven.

## Claude Code normalization

The input batch is copied before modification because the Collector may fan it
out to other consumers. The normalizer changes only these native span types:

| Native name | Canonical name | Canonical operation |
| --- | --- | --- |
| `claude_code.interaction` | `invoke_agent claude_code` | `invoke_agent` |
| `claude_code.llm_request` | `chat <model>` | `chat` |
| `claude_code.tool` | `execute_tool <tool>` | `execute_tool` |

All other Claude spans, including permission wait, tool execution, hooks, and
subagent descendants, retain their native names and hierarchy. The normalizer
adds provider/client/source attributes and maps `session.id` to
`gen_ai.conversation.id` on the interaction span when available.

## Privacy and security

Codex prompt content, tool arguments, and tool output are never copied into
synthetic spans. Safe length/count/status fields are retained. Raw telemetry is
still exported by the example pipeline, so operators must apply their own
retention, authorization, and redaction policies to that raw destination.

Recommended endpoint defaults:

- leave Codex `log_user_prompt=false`;
- leave Claude `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS`,
  `OTEL_LOG_TOOL_CONTENT`, and `OTEL_LOG_RAW_API_BODIES` disabled;
- authenticate and encrypt OTLP outside the local Compose test;
- filter user identity attributes if they are not required.

## Restart and delivery behavior

State is in memory. Collector shutdown drains incomplete turns, but a crash can
lose active state. Persistent state is deferred until restart continuity is a
demonstrated requirement; adding it would require an explicit storage contract,
schema versioning, and replay/deduplication policy.

The connector returns synchronous downstream errors during ingestion-triggered
flushes. Background finalization logs downstream failures because the original
receiver request is no longer active. Production deployments should use a
reliable downstream exporter with queue/retry support.

## Testing strategy

The ordinary, non-billable suite covers parsing, validation, deterministic IDs,
canonical trees, redaction, token mappings, status, out-of-order batches,
multi-turn splitting, bounds, shutdown drain, factory creation, and Claude
copy-on-normalize behavior. Race testing exercises the timer/consumer boundary.

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

The E2E is prepared and compiled by normal verification, but is not invoked by
the automated test command.

## Known limitations and future work

- Codex state does not persist across crashes.
- Multiple Collector replicas require consistent routing by conversation ID or
  shared state.
- The connector intentionally ignores coding-agent logs without a conversation ID.
- Only Codex log synthesis and Claude Code native-span normalization are implemented.
- Provider schemas are not stable APIs; fixtures and E2E should be rerun before
  upgrading pinned client or Collector versions.
- Upstream semantic-convention changes may replace some `coding_agent.*` fields.
