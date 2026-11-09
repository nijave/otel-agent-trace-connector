# Telemetry Gaps Implementation Plan (2026-11-08)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to carry out this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the gaps found on 2026-11-07/08 by comparing a live deployment's
canonical output (HyperDX/ClickHouse, Thanos) with the raw Claude Code, Codex,
and Pi telemetry and the local session files.

**Specs:**
- `docs/superpowers/specs/2026-11-08-claude-log-chat-spans-design.md` (Task 2)
- `docs/superpowers/specs/2026-11-08-ha-traceid-routing-design.md` (Task 1)

The other tasks are small enough that this plan is their record.

## Global Constraints

- **Prefer small data tradeoffs over new state.** Every change here keeps the
  traces edges stateless and adds no state to the logs edges. When a fix would
  need cross-batch or cross-signal state, take the best-effort stateless
  version and document the gap.
- Canonical output stays fail-closed: only keys in
  `internal/canonical/vocabulary.go`. New keys go there, in
  `docs/canonical-attributes.md` (the mirror test enforces it), and in each
  affected harness matrix.
- Do not mutate input batches (`MutatesData: false`).
- Each functional change lands with focused regression tests and the matching
  harness-matrix and README updates. One task = one commit.
- Run Go with `GOTOOLCHAIN=auto`. Run `GOTOOLCHAIN=auto ./scripts/check.sh`
  after each task. Do not run paid live e2e scripts; `scripts/e2e-routing.sh`
  costs nothing; run it.
- Fixtures use real key names and synthetic values: no real emails, account
  IDs, paths, or repository names.

## Evidence summary (live, 7 days to 2026-11-08)

| # | Finding | Evidence |
| --- | --- | --- |
| E1 | Claude Code cost absent from canonical output | Traces carry no cost; `claude_code.api_request` logs carry `cost_usd` and join to `llm_request` spans on `request_id` for 4,335 of 4,343 requests |
| E2 | Claude Code tool failures invisible on `execute_tool` | 115 `claude_code.tool.execution` sub-spans with status Error; their `execute_tool` parents are all Unset |
| E3 | Pi chat and tool spans have no `gen_ai.conversation.id` | 0 of 1,337 Pi chat spans; every raw Pi span carries `sessionId` |
| E4 | Pi subagent marker dropped | Raw `llm-generation [subagent] [request]` vs `[main]`; no canonical marker |
| E5 | Claude Code retry count dropped | `attempt` on `llm_request` and `api_error` drops; no canonical key |
| E6 | Codex failures never set chat status | 107 of 107 Codex chat spans Unset; a failed `response.completed` with only `error.message` drops |
| E7 | Metrics path drops delta data | Claude Code exports delta by default; `prometheusremotewrite` counts them as failed translations (2,760 of 2,776 in 7 days); only `target_info` reaches the backend |
| E8 | Stale Claude Code docs | `request_id` / `gen_ai.response.id` present on 100% of `llm_request` spans (docs say absent); Claude Code has a per-request cost on its logs (docs say none) |
| E9 | HA example pins all stateless traffic to one tier-2 pod | See the HA spec |
| E10 | Codex turns emitted twice when both signals arrive; native edge covers less | Same conversations from both edges: logs edge 27 turns / 94 chats / 82 with TTFC / 6.35M input tokens; native edge 17 turns / 13 chats / 0 with TTFC / 11.75M input tokens |

---

### Task 1: HA routing by statefulness (E9)

Build the HA spec exactly, including its "facts to verify" section and the
zero-cost routing e2e. Also add a metrics note to
`docs/multi-instance-ha.md`: metrics through tier 1 need
`routing_key: streamID` when tier 2 runs `deltatocumulative` (Task 8).

- [ ] Verify the three upstream facts; record citations in the commit body.
- [ ] Update `examples/k8s-ha/` (tier-1 routing connector, two exporters).
- [ ] Extend the routing e2e with a Claude native-traces replay and the spread
      assertions; run `scripts/e2e-routing.sh`.
- [ ] Update `docs/multi-instance-ha.md`.

### Task 2: Claude Code chat spans from logs (E1, E8)

Build the Claude log chat-spans spec: the `internal/claudelogs` edge, the
`claude_code.chat_source` config field, the traces-edge suppression, tests,
fixture, conformance registration, and docs. Re-audit
`docs/harnesses/claude-code.md` against the evidence in the spec (Claude Code
2.1.319) and correct every stale statement found (E8).

- [ ] Config field and validation tests.
- [ ] `claudelogs` edge with unit, fixture, and conformance tests.
- [ ] Claude traces edge drops `llm_request` when `claude_code.chat_source: logs`.
- [ ] Docs: claude-code matrix (new Logs mapping section), canonical-attributes
      cost row, README configuration section.

### Task 3: Claude Code tool failures on `execute_tool` (E2)

In the Claude traces edge, when a canonical `execute_tool` span and its
`claude_code.tool.execution` child with status Error are in the same batch,
set the `execute_tool` span's status to Error and copy the child's status
message. This is best-effort by design: when the child arrives in another
batch, the parent keeps Unset, and the child still carries the Error. Build
the parent→child lookup per batch only (stateless).

- [ ] Tests: same-batch propagation; child in another batch (no change, no
      error); successful child (no change); parent already Error (unchanged).
- [ ] Update the `success` row of the claude-code matrix.

### Task 4: Pi conversation ID on every span (E3)

Map each raw Pi span's own `sessionId` to `gen_ai.conversation.id` on every
canonical span type, not only `chat-turn`. A native `gen_ai.conversation.id`
wins if present. Stateless.

- [ ] Tests: a batch without its `chat-turn` still yields conversation IDs on
      chat and tool spans; mixed sessions in one batch keep their own IDs.
- [ ] Update `docs/harnesses/pi.md` (the split-batch caveat goes away).

### Task 5: Pi subagent attribution (E4)

Map Pi's subagent marker onto `gen_ai.agent.id` the way the vocabulary
describes for other harnesses. Inspect the raw pi-telemetry fixture and source
to find the subagent's identifier (the span name carries `[subagent]`, and the
subagent's spans carry their own `sessionId`). Use that identifier for
`gen_ai.agent.id`; set `coding_agent.agent.parent_id` only when the same span
or batch states the parent without lookup state. Main-agent spans carry
neither key.

- [ ] Tests from a fixture with main and subagent generations.
- [ ] Update `docs/harnesses/pi.md` and the `gen_ai.agent.id` paragraph in
      `docs/canonical-attributes.md`.

### Task 6: Retry attempt key (E5)

Add `coding_agent.request.attempt` (int, connector namespace) to the
vocabulary. Map Claude Code `attempt` from `llm_request` spans (traces source)
and from `api_error` logs (logs source, Task 2 edge). Other harnesses: record
"not provided" rows unless their wire carries an attempt count.

- [ ] Vocabulary, mirror doc, and provenance list.
- [ ] Mapping tests for both Claude sources; integer and string values.
- [ ] Matrix rows.

### Task 7: Codex failures set chat status (E6, E10)

1. In the Codex logs edge, a `response.completed` (or another terminal
   event) that carries `error.message` sets the turn's last chat span to status
   Error with that message. Check `docs/harnesses/codex.md` row "failed
   `response.completed` carrying only `error.message`". Apply the same rule in
   the Codex native traces edge if its wire carries the failure.
2. E10, investigation only: compare the two Codex edges on the pinned
   fixtures. Document in `docs/harnesses/codex.md` why the native edge yields
   fewer chat spans, no TTFC, and higher input-token totals for the same
   turns. Fix mapping bugs that are local and stateless (for example a wrong
   token key or a missed TTFC field). List anything larger as a follow-up in
   the doc. Strengthen the README warning: with both Codex signals reaching the
   connector, each turn appears twice. The client-side fix is to leave
   `otel.trace_exporter` unset.

- [ ] Error-status tests (logs edge; native edge if applicable).
- [ ] Parity notes, local fixes with tests, README warning.

### Task 8: Metrics path converts delta to cumulative (E7)

- [ ] Add `deltatocumulativeprocessor` (contrib, v0.163.0) to
      `builder-config.yaml`.
- [ ] Add a metrics pipeline example (`deltatocumulative` →
      `prometheusremotewrite`) to the reference collector config or examples,
      with a comment that the processor is stateful per series and needs
      `streamID` routing in HA (Task 1 doc note).
- [ ] Replace "Connector mapping: Not applicable" in the claude-code metrics
      section, and update `docs/metrics.md` / `docs/otel-signals.md`, to say
      the distribution ships a metrics path and why it converts delta to cumulative.
- [ ] Build check: `scripts/check.sh` (and the OCB build it runs) passes.

### Final: integration

- [ ] `GOTOOLCHAIN=auto ./scripts/check.sh` and `scripts/e2e-routing.sh` pass.
- [ ] Every evidence row E1–E10 maps to a commit or a documented follow-up.
- [ ] Open one PR; its body lists the follow-ups and the deployment change
      `claude_code.chat_source: logs` needs (both connector instances).
