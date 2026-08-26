# Usage-Drift Mappings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Map two cost-relevant usage counters that upstream harnesses now
emit but the connector drops: Codex `cache_write_token_count` and OpenCode
`ai.usage.reasoningTokens` on `ai.streamText` root spans.

**Architecture:** Both changes extend existing guarded-copy tables/branches
in stateless mapping code. No new components, config, or vocabulary keys —
`gen_ai.usage.cache_creation.input_tokens` and
`gen_ai.usage.reasoning.output_tokens` already exist in
`internal/canonical/vocabulary.go`. Each change lands as one commit with its
test, conformance signal, and matrix-doc update.

**Tech Stack:** Go (Collector pdata), testify, the repo's conformance
harness (`internal/canonical/conformance.go`).

**Spec:** [docs/superpowers/specs/2026-08-24-usage-drift-mappings-design.md](../specs/2026-08-24-usage-drift-mappings-design.md)

## Global Constraints

- Work on branch `feat/usage-drift-mappings` (stacked on
  `docs/use-case-and-goals`, PR #50 — it rewrote the matrix rows these
  tasks touch).
- The system Go is 1.25 with `GOTOOLCHAIN=local`; prefix every `go`
  command with `GOTOOLCHAIN=auto` (the module requires ≥ 1.27).
- Connector tests run from `connector/codingagentconnector/`.
- Canonical attributes are a closed vocabulary; these tasks add NO new
  keys. Matrix docs must state wire conditions inline per AGENTS.md
  ("Matrix docs record conditional coverage").
- Run `./scripts/check.sh` (with `GOTOOLCHAIN=auto`) before any push.
- The pinned Codex fixture (`internal/codex/testdata/codex-native-logs.json`)
  predates `cache_write_token_count` and MUST NOT be hand-edited; the file
  pins a real capture. Conformance `Sum` signals compare totals, so a key
  absent from both raw and output passes at 0 == 0.

---

### Task 1: Codex — map `cache_write_token_count`

**Files:**
- Change: `connector/codingagentconnector/internal/codex/trace.go:28-35`
  (the `tokenUsageAttrs` table)
- Change: `connector/codingagentconnector/conformance_test.go:261-273`
  (the codex `Signals` list)
- Test: `connector/codingagentconnector/internal/codex/trace_test.go`
- Docs: `docs/harnesses/codex.md`

**Interfaces:**
- Consumes: `tokenUsageAttrs` (`[]struct{ source, dest string }`) — the
  single source of truth for chat-span usage; `copyIntAttr` guard-copies
  each pair, and `hasTokenUsage` treats any listed source key as
  usage-bearing (this change makes a cache-write-only completion count as
  usage-bearing, which is correct: it keeps the completion instead of
  dropping it as a timing-only duplicate).
- Produces: chat spans carrying `gen_ai.usage.cache_creation.input_tokens`
  when the wire carries `cache_write_token_count`.

- [ ] **Step 1: Write the failing test**

Append to `connector/codingagentconnector/internal/codex/trace_test.go`,
following the `TestUsageStaysOnChatSpansNotRoot` idiom:

```go
func TestBuildTraceMapsCacheWriteTokens(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{
				"event.kind":              "response.completed",
				"model":                   "glm-test",
				"input_token_count":       5,
				"cache_write_token_count": 42,
			}),
		},
	}
	spans := mustBuildTrace(t, turn, "completed").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	chat := findSpan(t, spans, "chat glm-test")
	require.Equal(t, int64(42), attrInt(t, chat, "gen_ai.usage.cache_creation.input_tokens"))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/codex/ -run TestBuildTraceMapsCacheWriteTokens -v`
Expected: FAIL — the canonical key is absent from the chat span.

- [ ] **Step 3: Add the mapping row**

In `connector/codingagentconnector/internal/codex/trace.go`, extend
`tokenUsageAttrs`:

```go
var tokenUsageAttrs = []struct{ source, dest string }{
	{"input_token_count", "gen_ai.usage.input_tokens"},
	{"output_token_count", "gen_ai.usage.output_tokens"},
	{"cached_token_count", "gen_ai.usage.cache_read.input_tokens"},
	{"cache_write_token_count", "gen_ai.usage.cache_creation.input_tokens"},
	{"tool_token_count", "gen_ai.usage.total_tokens"},
	{"reasoning_token_count", "gen_ai.usage.reasoning.output_tokens"},
}
```

- [ ] **Step 4: Run the codex package tests**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/codex/ -v`
Expected: PASS, including the new test and the untouched fixture tests
(the pinned capture carries no `cache_write_token_count`, so existing
assertions do not change).

- [ ] **Step 5: Add the conformance signal**

In `connector/codingagentconnector/conformance_test.go`, in the codex
edge's `Signals` list, after the `cached_token_count` row:

```go
{RawKey: "cache_write_token_count", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
```

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test -run TestCanonicalConformance ./...`
Expected: PASS (the fixture lacks the raw key; totals compare 0 == 0).

- [ ] **Step 6: Update the matrix doc**

In `docs/harnesses/codex.md`:

1. Add a row to the "codex.sse_event (response.completed) → chat" table
   after the `cached_token_count` row:

```markdown
| `cache_write_token_count` | `gen_ai.usage.cache_creation.input_tokens` | mapped (absent when the provider's usage carries no cache-write field; upstream added the field after the pinned 0.144.1 capture) |
```

2. Delete this row from "Canonical keys with no Codex source":

```markdown
| `gen_ai.usage.cache_creation.input_tokens` | not provided by the pinned wire (upstream HEAD adds `cache_write_token_count`; unmapped until the pin bumps) |
```

3. In the "Wire drift" paragraph, remove `cache_write_token_count` from
   the list of unmapped additions (the other fields stay listed).

- [ ] **Step 7: Run both module test suites and commit**

```bash
GOTOOLCHAIN=auto go test ./... \
  && (cd connector/codingagentconnector && GOTOOLCHAIN=auto go test -race ./...)
git add connector/codingagentconnector/internal/codex/trace.go \
  connector/codingagentconnector/internal/codex/trace_test.go \
  connector/codingagentconnector/conformance_test.go \
  docs/harnesses/codex.md
git commit -m "feat(connector): map codex cache_write_token_count to cache-creation tokens"
```

---

### Task 2: OpenCode — map root reasoning tokens

**Files:**
- Change: `connector/codingagentconnector/internal/opencode/normalizer.go:119-131`
  (the `wireStreamText` branch) and `:139-150` (extract the doStream
  reasoning block into a helper)
- Change: `connector/codingagentconnector/conformance_test.go:335-343`
  (the opencode `Signals` list)
- Test: `connector/codingagentconnector/internal/opencode/normalizer_test.go`
- Docs: `docs/harnesses/opencode.md`, `docs/design.md`

**Interfaces:**
- Consumes: `intValue(attrs pcommon.Map, key string) (int64, bool)` —
  int/double-coercing reader already in `normalizer.go`.
- Produces: `copyReasoning(from, to pcommon.Map)` — writes
  `gen_ai.usage.reasoning.output_tokens` from `ai.usage.reasoningTokens`,
  falling back to `ai.usage.outputTokenDetails.reasoningTokens`; called
  from both the `wireStreamText` and `wireDoStream` branches.

- [ ] **Step 1: Write the failing test**

Append to `connector/codingagentconnector/internal/opencode/normalizer_test.go`:

```go
func TestNormalizerMapsRootReasoningTokens(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "opencode")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")
	root := ss.Spans().AppendEmpty()
	root.SetName("ai.streamText")
	root.Attributes().PutStr("session.id", "ses_r")
	root.Attributes().PutInt("ai.usage.reasoningTokens", 17)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	got, ok := out.Attributes().Get("gen_ai.usage.reasoning.output_tokens")
	require.True(t, ok, "root reasoning tokens must map")
	require.Equal(t, int64(17), got.Int())
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/opencode/ -run TestNormalizerMapsRootReasoningTokens -v`
Expected: FAIL — the key is absent on the root today.

- [ ] **Step 3: Extract the helper and call it from both branches**

In `connector/codingagentconnector/internal/opencode/normalizer.go`, add
next to `copyUsage`:

```go
// copyReasoning maps the step's reasoning counter onto the canonical key,
// falling back to the outputTokenDetails spelling. Both ai.streamText and
// ai.streamText.doStream carry it on the wire (opencode >= 1.18.21).
func copyReasoning(from, to pcommon.Map) {
	reasoning, ok := intValue(from, "ai.usage.reasoningTokens")
	if !ok {
		reasoning, ok = intValue(from, "ai.usage.outputTokenDetails.reasoningTokens")
	}
	if ok {
		to.PutInt("gen_ai.usage.reasoning.output_tokens", reasoning)
	}
}
```

In the `case wireStreamText:` branch, after the existing
`copyUsage(wire.Attributes(), attrs)` line, add:

```go
		copyReasoning(wire.Attributes(), attrs)
```

In the `case wireDoStream:` branch, replace the inline block

```go
		reasoning, ok := intValue(wire.Attributes(), "ai.usage.reasoningTokens")
		if !ok {
			reasoning, ok = intValue(wire.Attributes(), "ai.usage.outputTokenDetails.reasoningTokens")
		}
		if ok {
			attrs.PutInt("gen_ai.usage.reasoning.output_tokens", reasoning)
		}
```

with:

```go
		copyReasoning(wire.Attributes(), attrs)
```

- [ ] **Step 4: Run the opencode package tests**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/opencode/ -v`
Expected: PASS. If `TestNormalizerFixtureReplay` fails because the pinned
raw fixture (`internal/opencode/testdata/opencode-native-traces.json`)
carries `ai.usage.reasoningTokens` on its `ai.streamText` span, the golden
canonical fixture gains the new key on the `invoke_agent opencode` span —
update the golden JSON to match the code's output (inspect the diff the
test prints; the change must be exactly one added attribute) and re-run.

- [ ] **Step 5: Add the conformance signal**

In `connector/codingagentconnector/conformance_test.go`, in the opencode
edge's `Signals` list, after the `ai.usage.totalTokens` row:

```go
{RawKey: "ai.usage.reasoningTokens", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
```

Unscoped on purpose: after this change every raw occurrence (root or
doStream) maps 1:1, so trace-wide totals match; scoping to
`ai.streamText.doStream` like the other counters would fail once the raw
fixture carries the key on the root too.

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test -run TestCanonicalConformance ./...`
Expected: PASS.

- [ ] **Step 6: Update the docs**

In `docs/harnesses/opencode.md`, replace the `ai.streamText` table row

```markdown
| `ai.usage.reasoningTokens` / `ai.usage.outputTokenDetails.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | not provided on this span (mapped on `doStream` only) |
```

with

```markdown
| `ai.usage.reasoningTokens` / `ai.usage.outputTokenDetails.reasoningTokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (fallback order as listed; absent for non-reasoning steps; on the wire from opencode 1.18.21) |
```

In `docs/design.md` (OpenCode normalization section), replace

```markdown
maps onto `gen_ai.usage.cache_read.input_tokens`; reasoning and token-detail
counters have no established canonical key and stay out. Each
```

with

```markdown
maps onto `gen_ai.usage.cache_read.input_tokens`, and
`ai.usage.reasoningTokens` (with its `outputTokenDetails` fallback) maps
onto `gen_ai.usage.reasoning.output_tokens`; other token-detail counters
stay out. Each
```

- [ ] **Step 7: Run both module test suites and commit**

```bash
GOTOOLCHAIN=auto go test ./... \
  && (cd connector/codingagentconnector && GOTOOLCHAIN=auto go test -race ./...)
git add connector/codingagentconnector/internal/opencode/normalizer.go \
  connector/codingagentconnector/internal/opencode/normalizer_test.go \
  connector/codingagentconnector/conformance_test.go \
  docs/harnesses/opencode.md docs/design.md
git commit -m "feat(connector): map opencode reasoning tokens on streamText roots"
```

If Step 4 changed the golden fixture, add
`connector/codingagentconnector/internal/opencode/testdata/opencode-canonical.otlp.json`
(or the actual golden filename in that directory) to the same commit.

---

### Task 3: Full check and push

- [ ] **Step 1: Run the full unpaid check suite**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: `ALL CHECKS PASSED`.

- [ ] **Step 2: Push the branch**

```bash
git push -u origin feat/usage-drift-mappings
```

Open a draft PR only when the maintainer asks; note in its body that the
branch stacks on `docs/use-case-and-goals` (PR #50) and rebases onto main
once that merges.

## Explicitly out of scope

- Codex `service_tier`, `model_reasoning_effort`, and the tool-result
  detail fields (`tool_namespace`, `output_truncated`, …): no canonical
  keys exist for them; adding vocabulary keys is a contract change nobody
  needs yet. The codex.md "Wire drift" note keeps recording them.
- Bumping the pinned Codex e2e version (0.144.1): the responses-proxy
  compatibility question makes that its own task.
- The unused `opentelemetry.instrumentation.genai` scope prefix: removal
  is a maintainer call recorded in docs/design.md.
