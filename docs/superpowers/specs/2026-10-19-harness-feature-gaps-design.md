# Harness feature gaps design (2026-10)

**Goal:** Close the gaps the 2026-10-19 harness audits left open that need a
design decision rather than a one-key mapping: a signal switch on the
connector, a canonical cost key, subagent attribution, Cursor Grok Bot tool
spans, a Codex native-traces edge, and two audit follow-ups (Claude
`organization.id`, the Cursor artifact event rename).

**Status:** design approved in chat 2026-10-19. This spec is the record for
the implementation plan that follows. The one-key mappings from the same
audits (tool call ids, max_tokens, Pi TTFT, identity email keys, the
OpenHands wrapper fold) are landing in PRs #104 through #113 from a parallel
session and are out of scope here except where a section builds on them.

## Background

The doc audits merged on 2026-10-19 (PRs #96 through #101) refreshed every
`docs/harnesses/*.md` matrix against the bumped harness pins. Reading the
matrices against the edge code found two kinds of gap: wire keys with an
existing canonical home that no edge mapped, and signals or keys with no
canonical home at all. The first kind is in flight elsewhere. This spec covers
the second kind, plus one operator control the Codex traces edge makes
necessary.

Facts this design rests on:

- Codex 0.163.0 exports its internal `tracing` spans once the operator sets
  `otel.trace_exporter`. The exporter filter drops only `h2`, `tokio`,
  and `runtime` targets, so TRACE-level bookkeeping spans export too. The
  scope name is the resource `service.name` (`codex_exec` for `codex exec`).
  One `session_task.turn` span per turn carries `thread.id`, `turn.id`,
  `model`, `codex.turn.reasoning_effort`, and seven
  `codex.turn.token_usage.*` totals. Under it, a span renamed to the response
  event type carries native `gen_ai.usage.input_tokens`,
  `gen_ai.usage.cache_read.input_tokens`,
  `gen_ai.usage.cache_write.input_tokens`, `gen_ai.usage.output_tokens`,
  `codex.usage.reasoning_output_tokens`, `codex.usage.total_tokens`, and
  `codex.request.reasoning_effort`. `mcp.tools.call` spans carry `tool.name`,
  `tool.call_id`, `conversation.id`, and `server.address`/`server.port`.
  Built-in tool results ride as `codex.tool_result` span events (`tool_name`,
  `call_id`, `duration_ms`, `success`) on the tool dispatch spans. The
  usage-bearing `response.completed` log event comes from a bare task
  with no enclosing span, so it never reaches traces. The trace id is per
  app-server request, so per turn, not per conversation. Sources: codex-rs at
  tag `rust-v0.163.0`, `otel/src/provider.rs`, `core/src/tasks/mod.rs`,
  `core/src/session/turn.rs`, `core/src/mcp_tool_call.rs`,
  `otel/src/tool_result.rs`, `otel/src/events/session_telemetry.rs`.
- Three wires carry a per-request cost: Pi 0.1.28 nests a `cost` object
  (`input`, `output`, `cacheRead`, `cacheWrite`, `total`) inside the
  serialized `usage` attribute; OpenHands SDK 1.56.0 writes
  `gen_ai.usage.cost` (double) on the `llm.<model>` wrapper span; Copilot CLI
  1.0.100 writes `github.copilot.cost` (double) on chat and invoke_agent
  spans. The GenAI semconv registry at the audited commit defines no cost
  attribute.
- Subagent identity on the wire: Claude Code documents `agent_id` and
  `parent_agent_id` on llm_request and tool spans; Codex 0.163.0 stamps
  `agent_name` on `codex.tool_result` (`/root` for the main agent, else the
  subagent's path, nickname, or conversation UUID); the OpenHands SDK writes
  `is_delegate`, `task_id`, `subagent_type`, and `parent_session_id` under
  `lmnr.association.properties.metadata.` on a delegate's spans.
- Cursor's `tool_calls` family is metrics-only on every surface. The
  `grok_bot_agent_actions` log family carries `grok_bot_tool_result` (builtin
  tools) and `grok_bot_mcp_tool_call` (MCP tools) rows with `cursor.tool.name`,
  `cursor.tool.status` (MCP row), `cursor.grok_bot.tool_result.outcome`
  (builtin row), `cursor.grok_bot.tool_call.id`, and a per-row duration
  (`cursor.grok_bot.tool_result.duration_ms`,
  `cursor.grok_bot.mcp.duration_ms`). Source: the Cursor OTel wire reference,
  checked 2026-10-19.
- The e2e collector config already runs two connector instances, one in a
  logs pipeline and one in a traces pipeline. Pipeline wiring is the existing
  way an operator picks which signals a connector instance consumes.

## Decisions

- **Signals are an explicit config list, not only pipeline wiring.** A
  single shared config in a fleet makes the connector's intent visible in one
  place, and an operator whose harnesses send both signals can turn one off
  without re-plumbing pipelines. Pipeline wiring stays the other mechanism and
  the README names both.
- **Cost lives in the connector namespace as `coding_agent.usage.cost`.**
  The registry has no cost key, so a `gen_ai.` spelling would invent one that
  a later registry key could contradict. The connector namespace already
  holds `coding_agent.request.service_tier` for the same reason. The value is
  a USD double copied verbatim; no currency key, because every source today
  reports dollars and only Copilot hints at a currency.
- **Cost is each harness's own estimate.** Cursor's docs say its cost "is a
  best-effort estimate, not an invoice"; Pi's comes from a model-pricing
  table in the extension; OpenHands computes it from LiteLLM pricing. The
  vocabulary doc says so, and the key is for trend and comparison queries,
  not billing.
- **Subagent attribution adds one key, `coding_agent.agent.parent_id`, and
  no spans.** `gen_ai.agent.id` already exists for the agent that produced a
  span. A parent key makes a subagent's spans joinable to the agent that
  spawned it. Synthesizing a nested `invoke_agent` span per subagent would
  need grouping logic in every edge and new correlation state in the two logs
  edges; that is real complexity for a shape no consumer has asked for.
- **Codex's main agent keeps `/root` as its `gen_ai.agent.id`.** The value is
  what the wire says and distinguishes the main agent from subagents without
  the connector inventing a name.
- **Grok Bot tool rows become `execute_tool` spans; other surfaces stay
  without tool spans.** Only the Grok Bot surface records tool calls as logs,
  and only with Action Recording on. The matrix records the surface
  condition.
- **Codex native traces get their own edge; the logs edge keeps its shape.**
  The two signals carry different facts (traces have per-turn totals and MCP
  server identity; logs have TTFT and service tier) and an operator picks one
  with the signal switch or pipeline wiring. The connector does not dedupe
  across signals. Running both into one connector produces two canonical
  traces per turn, and the docs say so.
- **The Codex traces edge keeps Codex's trace ids.** They are per turn, so a
  conversation is one canonical trace per turn, all sharing
  `gen_ai.conversation.id`.
  Rewriting trace ids to join them would lose the link to Codex's own spans
  in the raw pipeline and to any `TRACEPARENT` the operator set.
- **The Codex traces edge is stateless.** Every span it keeps carries what
  it needs or has its parent in the same batch; the turn span closes last, so
  a batch of children without their turn is possible and handled the way the
  Pi edge handles it: children keep their wire parent id and the backend
  reattaches them.

## Design

### 1. Signal switch

`codex.Config` (aliased as the connector `Config`) gains:

```go
Signals []string `mapstructure:"signals"` // default: ["logs", "traces"]
```

`Validate` rejects an empty list, an unknown value, or a duplicate. Two
helpers, `ConsumesLogs()` and `ConsumesTraces()`, read it. `logsRouter.
ConsumeLogs` returns nil before touching any edge when logs are off;
`tracesRouter.ConsumeTraces` does the same for traces. The routers are still
constructed, so `Start` and `Shutdown` keep their shape and the conformance
registry's edge count does not change. A connector instance with logs off in
a logs pipeline is a legal no-op: the records continue to the pipeline's
other exporters because the connector never mutates or drops its input.

Tests: config validation table; a logs router with `signals: [traces]` emits
nothing for a Codex fixture; a traces router with `signals: [logs]` emits
nothing for a Claude fixture; defaults emit both.

Docs: README configuration section gets the key, its default, and the
sentence that pipeline wiring is the other way to select signals.

### 2. Cost key

`internal/canonical/vocabulary.go` gains `coding_agent.usage.cost` after
`coding_agent.request.service_tier`; `docs/canonical-attributes.md` mirrors it
(the mirror test enforces this) and gains a paragraph on semantics. The key is
a double, USD, chat spans only, absent when the wire carries no cost.

| Edge | Source | Rule |
|---|---|---|
| Pi | `usage` object → `cost` → `total` | map when the member parses as a number; 0.1.9's flat `usage.cost.total` is the fallback key |
| OpenHands | `gen_ai.usage.cost` on the `llm.<model>` wrapper | PR #106 folds the wrapper into the inner chat span and carries cache counts across; cost rides the same path, written only when the inner span lacks it |
| GenAI (Copilot) | `github.copilot.cost` on chat spans | copy when present and the span's operation is `chat`; the invoke_agent copy drops with the rest of the vendor namespace |

Codex, Claude Code, OpenCode, Cursor, Strands, openai-v2, and util-genai have
no per-request cost on the wire; each matrix gets a "not provided" row.

Tests: per-edge unit tests for present, absent, and non-numeric values; the
cross-harness conformance registry gains a Presence signal for Pi
(`usage`/`cost.total` needs a nested-field read, so the Signal type's
`RawField` gains dotted-path support or the test reads `cost` as the field
and checks presence only), OpenHands, and Copilot.

### 3. Subagent attribution

`coding_agent.agent.parent_id` joins the vocabulary after `gen_ai.agent.id`'s
connector-namespace peers. Mapping:

| Edge | `gen_ai.agent.id` | `coding_agent.agent.parent_id` | Span types |
|---|---|---|---|
| Claude Code | `agent_id` (PR #109) | `parent_agent_id`, verbatim when non-empty | llm_request, tool, and sub-spans that carry them |
| Codex logs | `agent_name` on `codex.tool_result`, verbatim | not on the wire | execute_tool only |
| OpenHands | `…metadata.subagent_type` | `…metadata.parent_session_id` | the delegate group's invoke_agent root |

A span whose wire omits the key gets nothing. The OpenHands delegate root is
the synthetic root the edge already builds for a group claimed by
`is_delegate`; the two values come from any kept child that carries them,
first match in deterministic order, the same way the root inherits `session_id`.

Tests: Claude parent id present and absent; Codex `/root` and a subagent
path; OpenHands delegate group with both keys, delegate group with neither,
and a non-delegate group that must carry neither. Matrix rows move from
dropped to mapped with their absence conditions; `docs/canonical-attributes.md`
documents the new key.

### 4. Cursor Grok Bot tool spans

Builds on PR #104 (events named from the wire event name; `Event.Name`).
`appendToolSpans` in `internal/cursor/trace.go` turns each
`grok_bot_tool_result` and `grok_bot_mcp_tool_call` event into a span:

- name `execute_tool <cursor.tool.name>` (bare `execute_tool` when the name
  is missing), kind internal, parent the burst root, span id derived from
  `tool:` plus the event id;
- start = timestamp minus the row's duration (`…tool_result.duration_ms` or
  `…mcp.duration_ms`), end = timestamp; a row without a duration is a
  zero-length span;
- attributes: the three required keys, `coding_agent.source.event` = the
  event name, `gen_ai.tool.name`, `gen_ai.tool.call.id` ←
  `cursor.grok_bot.tool_call.id` when non-empty, `gen_ai.tool.type` =
  `builtin` for the tool-result row and `mcp` for the MCP row;
- Status Error with message `tool execution failed` when the builtin row's
  `outcome` is `error` or the MCP row's `cursor.tool.status` is `failure`;
  `denied` and `cancelled` stay unset status (the call did not fail, it did
  not run);
- every other attribute drops.

`appendRootEvents` skips the two record names so they no longer land as
root events. A builtin row that re-runs after a stream failure produces two
spans sharing a call id, which the wire reference says to expect.

Tests: a hand-built fixture under `internal/cursor/testdata/` with one
burst holding an `api_request`, one builtin tool row with an error outcome,
one MCP row with a success status, and one row without a duration; unit
tests on name, bounds, status, attributes, and the root-event exclusion; the
conformance registry gains a Presence signal on `cursor.tool.name`. The
matrix gains a `grok_bot_tool_result` / `grok_bot_mcp_tool_call →
execute_tool` table with the surface and Action Recording conditions, and
the `gen_ai.tool.*` "not provided" row changes to "mapped (Grok Bot surface
only)". No validator change: Cursor has no live stack.

### 5. Codex native traces edge

New package `internal/codextraces`, stateless, constructed by
`newTracesRouter` after the existing five edges. The cross-harness
conformance registry's named mirror gains `codextraces`.

**Claiming.** A resource group belongs to this edge when a scope's name
equals the resource `service.name` and any span in it carries the name
`session_task.turn`, `session_task.compact`, `session_task.review`,
`codex.exec`, or a name starting with `op.dispatch.`. The GenAI edge keeps
its behavior: its scope prefixes never match a Codex service name, so the
GenAI defer list needs no new entry.

**Mapping.**

| Wire span | Canonical | Attributes |
|---|---|---|
| `session_task.turn` (and `.compact`, `.review`) | `invoke_agent codex`, parent cleared | `gen_ai.operation.name`, `gen_ai.agent.name` = `codex`, `gen_ai.provider.name` = `openai`, `gen_ai.conversation.id` ← `thread.id`, `gen_ai.request.model` ← `model`, `gen_ai.request.reasoning.level` ← `codex.turn.reasoning_effort`, `gen_ai.usage.input_tokens` ← `codex.turn.token_usage.input_tokens`, `cache_read` ← `cached_input_tokens`, `cache_write` ← `cache_write_input_tokens`, `output` ← `output_tokens`, `reasoning.output` ← `reasoning_output_tokens`, `total` ← `total_tokens`; `coding_agent.source.event` = wire name |
| the child of `handle_responses` that carries `gen_ai.usage.input_tokens` | `chat <model>` | native `gen_ai.usage.input_tokens`, `cache_read.input_tokens`, `cache_write.input_tokens`, `output_tokens` kept; `codex.usage.reasoning_output_tokens` → `gen_ai.usage.reasoning.output_tokens`; `codex.usage.total_tokens` → `gen_ai.usage.total_tokens`; `codex.request.reasoning_effort` → `gen_ai.request.reasoning.level`; `gen_ai.request.model` from the nearest kept turn in the batch, else absent and the span is bare `chat`; `gen_ai.provider.name` = `openai` |
| `mcp.tools.call` | `execute_tool <tool.name>` | `gen_ai.tool.name` ← `tool.name`, `gen_ai.tool.call.id` ← `tool.call_id`, `gen_ai.tool.type` = `mcp`, `server.address`, `server.port` kept, Status passes through |
| any span carrying a `codex.tool_result` event | `execute_tool <tool_name>` | `gen_ai.tool.name` ← event `tool_name`, `gen_ai.tool.call.id` ← event `call_id`, `gen_ai.tool.type` = `builtin`, Status Error (`tool execution failed`) when event `success` is false; the edge removes the event itself |
| everything else | dropped | |

Every kept span rebuilds its attribute map from scratch (the Pi pattern), so
tracing-opentelemetry's `code.*`, `thread.*`, `busy_ns`, and `idle_ns` never
reach output. Kept children whose wire parent is absent from the batch
reparent to the batch's turn root when one exists; otherwise they keep their
parent id. Resource filtering is `canonical.FilterResource` as elsewhere.
`coding_agent.source` = `native`, `coding_agent.client.name` = `codex`,
`coding_agent.client.version` ← resource `service.version`.

**Logs edge.** `appendToolSpans` in `internal/codex/trace.go` writes
`gen_ai.tool.call.id` from `call_id` when non-empty (the matrix row moves to
mapped).

**Fixture.** One live Codex e2e run with `trace_exporter = { otlp-http = {
endpoint = "http://collector:4318/v1/traces", protocol = "binary" } }` added
to `e2e/codex/config.toml`. Slice the run's `raw-traces.json` to one turn and
commit it as `internal/codextraces/testdata/codex-native-traces.json` with a
generated canonical fixture. The e2e config change stays: the Codex
stack then exercises both edges, and the validator's Codex rule gains the
traces root (it keys on `invoke_agent codex` and accepts either
`coding_agent.source`). The e2e collector config wires the traces pipeline
to the `coding_agent/claude` instance already, so the capture needs no
collector config change.

**Docs.** `docs/harnesses/codex.md` Traces section: Connector mapping becomes
the matrix above; the Signal support row changes to "traces edge"; a
paragraph on per-turn trace ids and on running both signals.
`docs/otel-signals.md` and `docs/harnesses.md` Codex rows follow (PR #111
is already touching those files for other reasons; this lands after it).

### 6. Audit follow-ups

Two decisions the parallel session's briefs left to a human, resolved on
2026-10-20:

- **Claude Code `organization.id` → `coding_agent.team.id`**, under
  `capture_identity`, on the interaction root the same way `user.id` maps,
  verbatim when non-empty. Cursor's `cursor.team.id` is also an
  organization-scale grouping, so the key already means "the billing-scale
  group". Tests: present with the flag on, absent with the flag off, absent
  when the wire omits it. The 2.1.304 capture carries no `organization.id`,
  so the fixture synthesizes one the way the Codex fixture synthesizes
  identity. Matrix row moves from dropped to mapped (absent when …), and the
  "Canonical keys with no Claude Code source" row for `coding_agent.team.id`
  goes away.
- **Cursor `cloud_agent_artifact` rename.** PR #104 names events from the
  wire event name, so the record once named `cloud_agent_artifact_created`
  now reaches canonical output as `cloud_agent_artifact`. The wire reference
  documents `cursor.cloud_agent.artifact` with no kind attribute, so there is
  no suffix to restore. `docs/harnesses/cursor.md` records the rename next
  to the `coding_agent.cursor.artifact.*` row, with the old name, the new
  name, and the reason, so anyone matching on the old name sees why it
  changed. No code change.

Three others need no change after checking: Codex `user.account_id`
already maps to `coding_agent.user.id` with the workspace-account caveat in
the matrix; VS Code Copilot Chat `user.name` is a display name with no
canonical home and stays dropped; and the `mprokopov/pi-otel-telemetry`
claims in `docs/harnesses/pi.md`, `docs/metrics.md`, `docs/otel-signals.md`,
and `docs/harnesses.md` match the repository (`0beaa10` is still `main`) and
the npm 1.0.0 tarball (checked 2026-10-20): 8 instruments, `tool.name` the
only label in 1.0.0, common `user.name`/`host.name`/`environment` attributes
only on `main`, the npm README's `llm.model` label claim contradicted by
source, and the peer dependency on the deprecated
`@mariozechner/pi-coding-agent`. The connector claims a different extension,
so none of this touches the Pi edge.

## Out of scope

- Deduplicating Codex turns across the logs and traces edges.
- Per-subagent `invoke_agent` spans or reparenting under them.
- A currency key or any cost normalization.
- Tool spans for Cursor surfaces other than Grok Bot.
- Claiming Cursor's `o11y-dev/opentelemetry-hooks` add-on.

## Landing order

PRs #104 through #113 merged on 2026-10-20 (main at `c47f124`), so every
section builds on `main` directly. Only #115 (OpenHands tool call id and
max_tokens) is still open; section 3 touches the same normalizer and lands
after it.

1. Signal switch and cost key.
2. Cursor Grok Bot tool spans, with the artifact rename note.
3. Subagent attribution, with Claude `organization.id`.
4. Codex traces edge and logs-edge `call_id`.

Each lands as its own PR off `main` with `./scripts/check.sh` green and, for
1, 3, and 4, the Codex, OpenHands, Pi, and Copilot live stacks run locally.
