# Usage-drift mappings — design

**Date:** 2026-08-24
**Status:** approved for planning; implementation pending maintainer go
**Plan:** [../plans/2026-08-24-usage-drift-mappings.md](../plans/2026-08-24-usage-drift-mappings.md)

## Motivation

Upstream audits (2026-08-24/25, per-harness source and package inspection)
found two cost-relevant usage counters that the wire now carries and the
connector silently drops:

1. **Codex** added `cache_write_token_count` to the usage-bearing
   `response.completed` record (upstream
   `codex-rs/otel/src/events/session_telemetry.rs:1020`; after the pinned
   0.144.1 capture). Cache writes bill at a premium, so dropping the count
   understates exactly the kind of cost the canonical vocabulary exists to
   track. The canonical destination
   (`gen_ai.usage.cache_creation.input_tokens`) already exists and five
   sibling counters already map.
2. **OpenCode** ≥ 1.18.21 emits `ai.usage.reasoningTokens` on the
   `ai.streamText` step root (upstream `sst/opencode` testdata), not only
   on the `ai.streamText.doStream` child the connector maps today. Reasoning
   tokens price differently; the canonical destination
   (`gen_ai.usage.reasoning.output_tokens`) already maps on the child.

## Goals

- Map `cache_write_token_count` → `gen_ai.usage.cache_creation.input_tokens`
  on Codex chat spans.
- Map `ai.usage.reasoningTokens` (fallback
  `ai.usage.outputTokenDetails.reasoningTokens`) →
  `gen_ai.usage.reasoning.output_tokens` on OpenCode `invoke_agent` roots,
  matching the existing chat-span mapping.
- Keep matrix docs honest per the conditional-coverage rule.

## Non-goals

- No new canonical vocabulary keys. Codex `service_tier`,
  `model_reasoning_effort`, and the tool-result detail fields
  (`tool_namespace`, `output_truncated`, length/origin fields) stay
  unmapped and recorded in codex.md's wire-drift note; mapping them means a
  contract change nobody needs yet.
- No Codex e2e pin bump (0.144.1 stays; the responses-proxy compatibility
  question makes a bump its own task).
- No change to the unused `opentelemetry.instrumentation.genai` scope
  prefix; its removal stays a maintainer call.

## Design

### Codex

One row added to `tokenUsageAttrs` in `internal/codex/trace.go` — the
table `copyIntAttr` guard-copies per chat span. Deliberate side effect:
`hasTokenUsage` iterates the same table, so a completion carrying only a
cache-write count now counts as usage-bearing and keeps its chat span
instead of dropping as the timing-only duplicate. That reading is correct —
such a record is the turn-completion record.

The pinned fixture predates the field and stays untouched (real capture).
Coverage comes from a synthetic unit test plus a conformance `Sum` signal;
`Sum` compares raw and canonical totals, so it passes 0 == 0 on the old
fixture and starts enforcing the mapping the moment a future capture
carries the key.

### OpenCode

The doStream branch's reasoning block extracts into a `copyReasoning`
helper called from both the `wireStreamText` and `wireDoStream` branches —
same fallback order, same int/double coercion via `intValue`. The
conformance signal for reasoning is deliberately unscoped (no
`RawSpanName`): after the change every raw occurrence maps 1:1, so
trace-wide totals match even when the wire carries the counter on both
parent and child. If the pinned raw fixture carries the key on its
`ai.streamText` span, the golden canonical fixture gains exactly one
attribute on the `invoke_agent opencode` span and updates in the same
commit.

## Testing

Per change: a failing-first unit test in the edge package, the edge's
package suite, a conformance signal row, and both module suites with
`-race`, then `./scripts/check.sh` before push.

## Risks

Low. Both mappings copy behind presence guards: an absent source key
writes nothing, so pre-change wires produce byte-identical output except
for the intended additions. The only behavioral edge is the Codex
timing-only-duplicate classification described above.

## Branch and sequencing

Branch `feat/usage-drift-mappings`, stacked on `docs/use-case-and-goals`
(PR #50) because both tasks edit matrix rows that PR rewrote; rebase onto
main after PR #50 merges. One commit per change so each maps to one cause
and one revert unit.
