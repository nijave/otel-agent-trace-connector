# Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every functional and documentation issue from the 2026-10-25 review while restoring per-trace accuracy, fail-closed content handling, bounded correlation state, and documentation that matches behavior.

**Architecture:** Keep the existing routers and stateless normalizers. Scope Codex's batch-local model lookup by trace ID, apply the shared `content.Strip` policy to Codex-native and Pi spans, stop retaining overflow dedupe keys, and drop GenAI spans that lack the operation required by the canonical contract. Finish with documentation-only corrections; no new configuration, vocabulary, storage, or deployment subsystem is needed.

**Tech Stack:** Go 1.27, OpenTelemetry Collector pdata (`ptrace`, `pcommon`, `plog`), testify, OpenTelemetry SDK manual metrics reader, Markdown, Docker Compose, the repository's live e2e scripts.

**Spec:** `docs/superpowers/specs/2026-10-25-review-remediation-design.md`. Read the full spec before starting; the plan implements its seven sections.

## Global Constraints

- Keep the canonical attribute vocabulary unchanged. These fixes require no new canonical keys and no `metadata.yaml` changes.
- Preserve raw-pipeline fidelity. Filtering applies only to canonical output; normalizers continue to advertise `MutatesData: false` and must not mutate their input batches.
- Keep the traces edges stateless. Do not add cross-batch model state, persistent dedupe state, or a storage extension.
- Prefer removal and existing mechanisms over new abstractions. Reuse `internal/content.Strip`; do not introduce a second event policy or a bounded-LRU subsystem.
- Preserve existing span IDs, trace IDs, timestamps, kinds, status, links, and hierarchy except where the current edge already deliberately changes them.
- Each functional change lands with focused regression tests and the minimum documentation update needed to keep the contract accurate.
- Run Go commands with `GOTOOLCHAIN=auto`; the repository requires Go 1.27 and the host may force an older local toolchain.
- Stage explicit paths only. Preserve the pre-existing untracked `.claude/` directory and all unrelated working-tree changes.
- Run `GOTOOLCHAIN=auto ./scripts/check.sh` before pushing. Live paid e2es run only in Task 7, once per stack, with credentials already supplied through the environment.

## Review Focus

1. **One OTLP batch can contain several traces and resource groups with different models.** Each chat must use only its own trace's turn model. Task 1 pins two traces, cross-resource isolation, earliest-turn selection, and turnless behavior.
2. **A recognized native span can carry a content event or an operational event with sensitive non-canonical attributes.** Canonical output must remove the content event and strip the operational event without mutating raw input. Task 2 pins both cases on Codex-native and Pi.
3. **A redelivery storm can continue after a turn or burst truncates.** Both `events` and `seen` must stay bounded, and overflow deliveries must not refresh finalization state. Task 3 pins bounds, retained-event dedupe, metrics, and finalization.
4. **A matched GenAI group can mix operation-bearing spans with spans that lack `gen_ai.operation.name`.** Emit only classifiable spans and no empty resource/scope groups. Task 4 pins singleton and mixed groups.
5. **The minimal HA deployment has no raw backend.** Its documentation must not promise standalone raw replay or a span finish-reason attribute. Task 6 checks the recovery precondition and metric wording explicitly.

---

### Task 1: Scope Codex native chat models by trace

**Files:**
- Modify: `connector/codingagentconnector/internal/codextraces/normalizer.go:106-198`
- Test: `connector/codingagentconnector/internal/codextraces/normalizer_test.go`
- Docs: `docs/harnesses/codex.md`

**Interfaces:**
- Consumes: existing `ContainsCodexSpans`, `addTurn`, `addResponse`, `normalizeOne`, and `findSpan` test helpers.
- Produces: `func batchTurnModels(input ptrace.Traces) map[pcommon.TraceID]string`; `normalizeSpan(span ptrace.Span, version, model string) spanKind` keeps its signature, but receives only the model for `span.TraceID()`.

- [ ] **Step 1: Write the failing multi-trace regression test**

Add to `normalizer_test.go`; its existing imports already include everything used:

```go
func TestResponseModelIsScopedToTrace(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)

	firstTurn := addTurn(spans)
	firstTurn.Attributes().PutStr("model", "model-a")
	addResponse(spans, "response.completed", 3, 1)

	traceTwo := pcommon.TraceID([16]byte{2})
	secondTurn := addTurn(spans)
	secondTurn.SetTraceID(traceTwo)
	secondTurn.SetSpanID(pcommon.SpanID([8]byte{11}))
	secondTurn.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(20 * time.Second)))
	secondTurn.SetEndTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(30 * time.Second)))
	secondTurn.Attributes().PutStr("model", "model-b")
	secondResponse := addResponse(spans, "response.completed", 3, 1)
	secondResponse.SetTraceID(traceTwo)
	secondResponse.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(22 * time.Second)))
	secondResponse.SetEndTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(26 * time.Second)))

	out := normalizeOne(t, input)
	require.Equal(t, "model-a", attrString(t, findSpan(t, out, "chat model-a"), "gen_ai.request.model"))
	require.Equal(t, "model-b", attrString(t, findSpan(t, out, "chat model-b"), "gen_ai.request.model"))
}
```

- [ ] **Step 2: Run the test and confirm the original defect**

Run from `connector/codingagentconnector/`:

```bash
GOTOOLCHAIN=auto go test ./internal/codextraces -run '^TestResponseModelIsScopedToTrace$' -count=1
```

Expected: FAIL with `span "chat model-b" not found` because both chats receive `model-a`.

- [ ] **Step 3: Implement per-trace batch lookup**

In `ConsumeTraces`, replace the single model lookup and its use:

```go
models := batchTurnModels(input)
// ...
kinds[keyOf(span)] = normalizeSpan(span, version, models[span.TraceID()])
```

Replace `batchTurnModel` with:

```go
// batchTurnModels returns each trace's earliest turn model. Response spans
// inherit only the model from their own trace; other turns in the OTLP batch
// are unrelated. Only claimed groups contribute.
func batchTurnModels(input ptrace.Traces) map[pcommon.TraceID]string {
	models := make(map[pcommon.TraceID]string)
	earliest := make(map[pcommon.TraceID]pcommon.Timestamp)
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		rs := input.ResourceSpans().At(i)
		if !ContainsCodexSpans(rs) {
			continue
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if !turnSpanNames[span.Name()] {
					continue
				}
				model := firstString(span.Attributes(), "model")
				if model == "" {
					continue
				}
				traceID := span.TraceID()
				if _, exists := models[traceID]; !exists || span.StartTimestamp() < earliest[traceID] {
					models[traceID] = model
					earliest[traceID] = span.StartTimestamp()
				}
			}
		}
	}
	return models
}
```

Do not add package-level state or change response-only behavior.

- [ ] **Step 4: Run the regression and existing Codex-native tests**

```bash
GOTOOLCHAIN=auto go test ./internal/codextraces -run '^(TestResponseModelIsScopedToTrace|TestResponseWithoutTurnStaysBareChat)$' -count=1
GOTOOLCHAIN=auto go test ./internal/codextraces -count=1
```

Expected: both commands PASS. The turnless chat remains bare `chat` with no model attribute.

- [ ] **Step 5: Add earliest-turn and cross-resource tests**

Add:

```go
func TestBatchTurnModelsUsesEarliestTurnPerTrace(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	late := addTurn(spans)
	late.SetSpanID(pcommon.SpanID([8]byte{11}))
	late.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(20 * time.Second)))
	late.Attributes().PutStr("model", "late-model")
	early := addTurn(spans)
	early.Attributes().PutStr("model", "early-model")

	models := batchTurnModels(input)
	require.Equal(t, "early-model", models[traceOne])
}

func TestResponseModelDoesNotCrossResourceGroups(t *testing.T) {
	input := ptrace.NewTraces()
	turn := addTurn(newCodexGroup(input))
	turn.Attributes().PutStr("model", "resource-model")
	children := newCodexGroup(input)
	response := addResponse(children, "response.completed", 3, 1)
	response.SetTraceID(pcommon.TraceID([16]byte{2}))

	out := normalizeOne(t, input)
	chat := findSpan(t, out, "chat")
	_, hasModel := chat.Attributes().Get("gen_ai.request.model")
	require.False(t, hasModel)
}
```

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/codextraces -run '^(TestBatchTurnModelsUsesEarliestTurnPerTrace|TestResponseModelDoesNotCrossResourceGroups)$' -count=1
```

Expected: PASS.

- [ ] **Step 6: Clarify the Codex matrix**

In `docs/harnesses/codex.md`, in the response-span mapping row for the turn model, use this status text:

```markdown
mapped from the earliest turn span carrying the same trace ID in this export (absent, and the span named bare `chat`, when that trace has no model-bearing turn span in the export; turns in other traces or resource groups never supply the model)
```

- [ ] **Step 7: Run focused verification and commit**

```bash
cd connector/codingagentconnector
GOTOOLCHAIN=auto go test ./internal/codextraces ./... -count=1
cd ../..
git diff --check
git add connector/codingagentconnector/internal/codextraces/normalizer.go connector/codingagentconnector/internal/codextraces/normalizer_test.go docs/harnesses/codex.md
git commit -m "fix(codex): scope chat models by trace"
```

Expected: tests PASS; the commit contains only the per-trace fix, tests, and matrix wording.

### Task 2: Filter Codex-native and Pi span events

**Files:**
- Modify: `connector/codingagentconnector/internal/codextraces/normalizer.go`
- Test: `connector/codingagentconnector/internal/codextraces/normalizer_test.go`
- Modify: `connector/codingagentconnector/internal/pi/normalizer.go`
- Test: `connector/codingagentconnector/internal/pi/normalizer_test.go`
- Docs: `docs/harnesses/codex.md`, `docs/harnesses/pi.md`

**Interfaces:**
- Consumes: `content.Strip(span ptrace.Span)` and the recognized-span decisions from each normalizer.
- Produces: no new API. Every emitted Codex-native and Pi span has canonical-only span/event attributes; raw input remains unchanged.

- [ ] **Step 1: Write the failing Codex-native event test**

Add to `internal/codextraces/normalizer_test.go`:

```go
func TestKeptSpanEventsUseCanonicalPolicy(t *testing.T) {
	input := ptrace.NewTraces()
	turn := addTurn(newCodexGroup(input))
	contentEvent := turn.Events().AppendEmpty()
	contentEvent.SetName("gen_ai.user.message")
	contentEvent.Attributes().PutStr("content", "sensitive prompt")
	operational := turn.Events().AppendEmpty()
	operational.SetName("codex.turn.status")
	operational.SetTimestamp(turn.StartTimestamp())
	operational.Attributes().PutStr("prompt", "sensitive prompt")
	operational.Attributes().PutStr("exception.type", "TurnError")

	root := findSpan(t, normalizeOne(t, input), "invoke_agent codex")
	require.Equal(t, 1, root.Events().Len())
	event := root.Events().At(0)
	require.Equal(t, "codex.turn.status", event.Name())
	_, hasPrompt := event.Attributes().Get("prompt")
	require.False(t, hasPrompt)
	exception, ok := event.Attributes().Get("exception.type")
	require.True(t, ok)
	require.Equal(t, "TurnError", exception.Str())
	require.Equal(t, 2, turn.Events().Len(), "raw input is unchanged")
	_, rawPrompt := turn.Events().At(1).Attributes().Get("prompt")
	require.True(t, rawPrompt)
}
```

- [ ] **Step 2: Run the Codex-native test and confirm the leak**

```bash
GOTOOLCHAIN=auto go test ./internal/codextraces -run '^TestKeptSpanEventsUseCanonicalPolicy$' -count=1
```

Expected: FAIL because two events survive and `prompt` remains.

- [ ] **Step 3: Apply `content.Strip` to kept Codex-native spans**

Add `"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/content"` to the imports and change the normalization loop:

```go
kind := normalizeSpan(span, version, models[span.TraceID()])
if kind != kindDrop {
	content.Strip(span)
}
kinds[keyOf(span)] = kind
```

Keep the existing whole-event removal for built-in `codex.tool_result` mappings. Run the focused test and package suite:

```bash
GOTOOLCHAIN=auto go test ./internal/codextraces -run '^TestKeptSpanEventsUseCanonicalPolicy$' -count=1
GOTOOLCHAIN=auto go test ./internal/codextraces -count=1
```

Expected: PASS.

- [ ] **Step 4: Write the failing Pi event test**

Add to `internal/pi/normalizer_test.go`; its existing imports include `context`, `ptrace`, and `require`:

```go
func TestPiKeptSpanEventsUseCanonicalPolicy(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName(scopePrefix)
	turn := scope.Spans().AppendEmpty()
	turn.SetName(turnSpanName)
	contentEvent := turn.Events().AppendEmpty()
	contentEvent.SetName("gen_ai.user.message")
	contentEvent.Attributes().PutStr("content", "sensitive prompt")
	operational := turn.Events().AppendEmpty()
	operational.SetName("llm.status")
	operational.SetTimestamp(turn.StartTimestamp())
	operational.Attributes().PutStr("langfuse.observation.output", "sensitive output")
	operational.Attributes().PutStr("exception.type", "GenerationError")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	root := findSpan(t, reassemble(sink), "invoke_agent pi")
	require.Equal(t, 1, root.Events().Len())
	event := root.Events().At(0)
	require.Equal(t, "llm.status", event.Name())
	_, hasOutput := event.Attributes().Get("langfuse.observation.output")
	require.False(t, hasOutput)
	exception, ok := event.Attributes().Get("exception.type")
	require.True(t, ok)
	require.Equal(t, "GenerationError", exception.Str())
	require.Equal(t, 2, turn.Events().Len(), "raw input is unchanged")
	_, rawOutput := turn.Events().At(1).Attributes().Get("langfuse.observation.output")
	require.True(t, rawOutput)
}
```

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/pi -run '^TestPiKeptSpanEventsUseCanonicalPolicy$' -count=1
```

Expected: FAIL because both events and the Langfuse output attribute survive.

- [ ] **Step 5: Apply `content.Strip` to recognized Pi spans**

Add `"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/content"` to the imports. In the native-span loop, after the `!native` branch and before recording children, add:

```go
content.Strip(span)
```

The resulting sequence is:

```go
child, native := normalizePiSpan(span, version)
if !native {
	foreign[span.SpanID()] = true
	continue
}
content.Strip(span)
if child {
	children[span.SpanID()] = true
}
```

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/pi -run '^TestPiKeptSpanEventsUseCanonicalPolicy$' -count=1
GOTOOLCHAIN=auto go test ./internal/pi -count=1
```

Expected: PASS.

- [ ] **Step 6: Document event handling**

Add to `docs/harnesses/codex.md` under the native connector mapping:

```markdown
#### Span events

Kept spans use the shared canonical event policy. Known GenAI content events
are removed whole. Other operational events keep their names and timestamps,
but their attributes are reduced to the canonical allowlist. The
`codex.tool_result` event used to construct a builtin tool span is removed
whole after its tool identity and outcome are mapped. Raw events remain
unchanged in the raw pipeline.
```

Add to `docs/harnesses/pi.md` under Connector mapping:

```markdown
#### Span events

Recognized spans use the shared canonical event policy in addition to
rebuilding their span attributes. Known content events drop whole; surviving
operational events keep only canonical attributes. This includes future or
extension-specific event attributes: they cannot bypass the canonical
allowlist. Raw events remain unchanged in the raw pipeline.
```

- [ ] **Step 7: Run conformance and commit**

```bash
cd connector/codingagentconnector
GOTOOLCHAIN=auto go test ./internal/codextraces ./internal/pi ./... -count=1
cd ../..
git diff --check
git add connector/codingagentconnector/internal/codextraces/normalizer.go connector/codingagentconnector/internal/codextraces/normalizer_test.go connector/codingagentconnector/internal/pi/normalizer.go connector/codingagentconnector/internal/pi/normalizer_test.go docs/harnesses/codex.md docs/harnesses/pi.md
git commit -m "fix(canonical): filter native span events"
```

Expected: all tests, including cross-harness conformance, PASS.

### Task 3: Bound Codex and Cursor dedupe state

**Files:**
- Modify: `connector/codingagentconnector/internal/codex/connector.go:188-230`
- Test: `connector/codingagentconnector/internal/codex/connector_test.go`
- Test: `connector/codingagentconnector/internal/codex/metrics_test.go`
- Modify: `connector/codingagentconnector/internal/cursor/connector.go:169-202`
- Test: `connector/codingagentconnector/internal/cursor/connector_test.go`
- Docs: `docs/design.md`

**Interfaces:**
- Consumes: existing `turnState.add`, `burstState.add`, `seenEvent`, redelivery-storm tests, and `otelcol_coding_agent_events_dropped`.
- Produces: unchanged function signatures. For either state type, `len(events) <= maxEvents` and `len(seen) <= len(events)` after every `add` call.

- [ ] **Step 1: Write failing bounds tests for both correlators**

Add to `internal/codex/connector_test.go`:

```go
func TestTruncationBoundsEventsAndFingerprints(t *testing.T) {
	turn := &turnState{}
	base := time.Now()
	for i := 0; i < 100; i++ {
		turn.add(testEvent("codex.api_request", base.Add(time.Duration(i)*time.Second), map[string]any{"sequence": fmt.Sprint(i)}), base, 1)
	}
	require.Len(t, turn.events, 1)
	require.Len(t, turn.seen, 1)
}
```

Add `fmt` to that test file's imports. Add to `internal/cursor/connector_test.go`:

```go
func TestTruncationBoundsEventsAndIDs(t *testing.T) {
	burst := &burstState{}
	base := time.Now()
	for i := 0; i < 100; i++ {
		burst.add(Event{Name: NameAPIRequest, EventID: fmt.Sprint(i), Timestamp: base.Add(time.Duration(i) * time.Second)}, base, 1)
	}
	require.Len(t, burst.events, 1)
	require.Len(t, burst.seen, 1)
}
```

Add `fmt` to the Cursor test imports too. Run both:

```bash
GOTOOLCHAIN=auto go test ./internal/codex ./internal/cursor -run '^TestTruncationBounds' -count=1
```

Expected: both FAIL with 100 dedupe entries instead of one.

- [ ] **Step 2: Stop retaining overflow dedupe keys**

In Codex's full-turn branch, retain only:

```go
	if len(t.events) >= maxEvents {
		// Overflow is discarded without changing finalization state. Its
		// fingerprint is deliberately not retained: seen shares the same
		// maxEvents bound as events. A redelivery may re-enter this branch,
		// but it remains side-effect free.
		t.truncated = true
		return
	}
```

In Cursor's full-burst branch, retain only:

```go
	if len(b.events) >= maxEvents {
		// Overflow is discarded without changing finalization state. Its ID
		// is deliberately not retained: seen shares the same maxEvents bound
		// as events. A redelivery may re-enter this branch, but it remains
		// side-effect free.
		b.truncated = true
		return
	}
```

Do not change the full-event path or the duplicate check before `add`.

- [ ] **Step 3: Update post-truncation expectations**

In Codex `TestPostTruncationCompletionDoesNotMutateTurnState`, replace the assertion requiring `turn.seenEvent(completion)` to be true with:

```go
require.False(t, turn.seenEvent(completion), "overflow fingerprints are not retained")
require.False(t, turn.completeSeen)
require.True(t, turn.completionAt.IsZero())
require.True(t, turn.lastSeen.Equal(now))
```

Keep the existing assertions that completion is absent from output and the turn finalizes as `timeout`. Update the test comment to say overflow redeliveries are side-effect free even though their fingerprints are not remembered.

In Cursor `TestPostTruncationRecordDoesNotMutateBurstState`, replace the `Contains(burst.seen, "ev-c")` assertion with:

```go
require.NotContains(t, burst.seen, "ev-c", "overflow IDs are not retained")
require.True(t, burst.lastSeen.Equal(now))
```

Keep the quiet-close and timestamp assertions unchanged.

- [ ] **Step 4: Prove retained-event dedupe and metrics still work**

Add to `internal/codex/metrics_test.go`:

```go
func TestTelemetryCountsRetainedDuplicateAfterTruncation(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.MaxEvents = 1
	instance, reader := newMeteredConnector(t, cfg, &traceSink{})
	base := time.Now()
	retained := makeLogs(testEvent("codex.user_prompt", base, map[string]any{"model": "gpt-test"}))
	overflow := makeLogs(testEvent("codex.api_request", base.Add(time.Second), map[string]any{"duration_ms": int64(10)}))
	require.NoError(t, instance.ConsumeLogs(context.Background(), retained))
	require.NoError(t, instance.ConsumeLogs(context.Background(), overflow))
	require.NoError(t, instance.ConsumeLogs(context.Background(), retained))

	rm := collectMetrics(t, reader)
	require.Equal(t, int64(1), counterValue(t, rm, "otelcol_coding_agent_events_dropped"))
}
```

For Cursor, add `sdkmetric "go.opentelemetry.io/otel/sdk/metric"` to the test imports and add:

```go
func TestTelemetryCountsRetainedDuplicateAfterTruncation(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.MaxEvents = 1
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	set := testSettings()
	set.TelemetrySettings.MeterProvider = provider
	edge, err := New(cfg, set, &traceSink{})
	require.NoError(t, err)
	instance := edge.(*cursorConnector)
	t.Cleanup(func() {
		require.NoError(t, instance.Shutdown(context.Background()))
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	base := time.Now()
	retained := makeCursorLogs(apiRequest(base, "a", "m", 1, 1))
	overflow := makeCursorLogs(apiRequest(base.Add(time.Second), "b", "m", 1, 1))
	require.NoError(t, instance.ConsumeLogs(context.Background(), retained))
	require.NoError(t, instance.ConsumeLogs(context.Background(), overflow))
	require.NoError(t, instance.ConsumeLogs(context.Background(), retained))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var dropped int64
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			if metric.Name != "otelcol_coding_agent_events_dropped" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, point := range sum.DataPoints {
				dropped += point.Value
			}
		}
	}
	require.Equal(t, int64(1), dropped)
}
```

Run both metric tests:

```bash
GOTOOLCHAIN=auto go test ./internal/codex ./internal/cursor -run '^TestTelemetryCountsRetainedDuplicateAfterTruncation$' -count=1
```

Expected: PASS; each retained duplicate counts once, while the overflow delivery is not counted as a recognized dedupe hit.

- [ ] **Step 5: Run state-machine and redelivery-storm tests**

```bash
GOTOOLCHAIN=auto go test ./internal/codex -run '^(TestTruncationBoundsEventsAndFingerprints|TestPostTruncationCompletionDoesNotMutateTurnState|TestTruncatedTurnStillFinalizesUnderRedeliveryStorm)$' -count=1
GOTOOLCHAIN=auto go test ./internal/cursor -run '^(TestTruncationBoundsEventsAndIDs|TestPostTruncationRecordDoesNotMutateBurstState|TestTruncatedBurstStillClosesQuietUnderRedeliveryStorm)$' -count=1
```

Expected: PASS. Overflow redeliveries do not refresh clocks or completion state even though their IDs are no longer remembered.

- [ ] **Step 6: Update the bounds documentation**

Replace the Codex dedupe paragraph in `docs/design.md` with:

```markdown
Within a live turn, redelivered retained events deduplicate by a content
fingerprint (event name, timestamp, and retained attributes). The event slice
and fingerprint set both bound at `max_events_per_turn`. Once the turn is
full, overflow events are discarded without retaining their fingerprints or
changing timestamps, completion state, or finalization clocks. A redelivered
overflow event may be examined again, but remains side-effect free; duplicate
retained events still deduplicate exactly. This preserves a hard per-turn
memory bound during at-least-once delivery storms.
```

Replace the Cursor dedupe paragraph with:

```markdown
Within a live burst, redelivered retained records deduplicate exactly on
`cursor.event.id`. The event slice and ID set both bound at
`max_events_per_turn`. Once the burst is full, overflow records are discarded
without retaining their IDs or changing timestamps or finalization clocks.
A redelivered overflow record may be examined again, but remains side-effect
free; duplicate retained records still deduplicate exactly.
```

- [ ] **Step 7: Run both packages and commit**

```bash
cd connector/codingagentconnector
GOTOOLCHAIN=auto go test -race ./internal/codex ./internal/cursor -count=1
cd ../..
git diff --check
git add connector/codingagentconnector/internal/codex/connector.go connector/codingagentconnector/internal/codex/connector_test.go connector/codingagentconnector/internal/codex/metrics_test.go connector/codingagentconnector/internal/cursor/connector.go connector/codingagentconnector/internal/cursor/connector_test.go docs/design.md
git commit -m "fix(correlation): bound overflow dedupe state"
```

Expected: focused race tests PASS.

### Task 4: Drop GenAI spans without an operation

**Files:**
- Modify: `connector/codingagentconnector/internal/genai/normalizer.go:79-113`
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`
- Docs: `docs/harnesses/genai-scopes.md`, `docs/design.md`

**Interfaces:**
- Consumes: existing scope claiming and `normalizeSpan`.
- Produces: canonical output only from matched-scope spans carrying `gen_ai.operation.name`; no empty emitted resource or scope groups.

- [ ] **Step 1: Replace the pass-through test with a failing drop test**

Replace `TestGenAINormalizerLeavesSpansWithoutOperationName` with:

```go
func TestGenAINormalizerDropsSpansWithoutOperationName(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "opentelemetry.instrumentation.openai_v2", "run")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all(), "an unclassified group emits no canonical batch")
}
```

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/genai -run '^TestGenAINormalizerDropsSpansWithoutOperationName$' -count=1
```

Expected: FAIL because the current edge emits one batch.

- [ ] **Step 2: Add the mixed-group test**

Add:

```go
func TestGenAINormalizerDropsOnlySpansWithoutOperationName(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "opentelemetry.util.genai.handler", "unclassified-only")
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "adhoc-agent")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opentelemetry.util.genai.handler")
	valid := ss.Spans().AppendEmpty()
	valid.SetName("chat")
	valid.Attributes().PutStr("gen_ai.operation.name", "chat")
	valid.Attributes().PutStr("gen_ai.request.model", "m")
	invalid := ss.Spans().AppendEmpty()
	invalid.SetName("application-work")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	require.Equal(t, 1, sink.all()[0].ResourceSpans().Len())
	require.Equal(t, 1, sink.all()[0].ResourceSpans().At(0).ScopeSpans().Len())
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	require.Equal(t, 1, spans.Len())
	require.Equal(t, "chat m", spans.At(0).Name())
	require.Equal(t, "native", attrString(t, spans.At(0), "coding_agent.source"))
}
```

Run it before implementation:

```bash
GOTOOLCHAIN=auto go test ./internal/genai -run '^TestGenAINormalizerDropsOnlySpansWithoutOperationName$' -count=1
```

Expected: FAIL because the unclassified-only resource group and the unclassified span in the mixed group both survive.

- [ ] **Step 3: Filter unclassified spans and empty groups**

In each matched scope, before the normalization loop, add:

```go
spans.RemoveIf(func(span ptrace.Span) bool {
	_, ok := span.Attributes().Get("gen_ai.operation.name")
	return !ok
})
```

After the normalization loop for that resource, add:

```go
rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool {
	return ss.Spans().Len() == 0
})
```

After the input-resource loop and before `output.SpanCount()`, add:

```go
output.ResourceSpans().RemoveIf(func(rs ptrace.ResourceSpans) bool {
	return rs.ScopeSpans().Len() == 0
})
```

The early return inside `normalizeSpan` remains as a defensive guard for direct calls; emitted batches have already been filtered.

- [ ] **Step 4: Keep scope-claim tests operation-bearing**

In `TestGenAINormalizerClaimsKnownScopes`, decorate the span returned by `newGroup`:

```go
span := newGroup(input, scope, "chat glm-4.7")
span.Attributes().PutStr("gen_ai.operation.name", "chat")
span.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
```

Run:

```bash
GOTOOLCHAIN=auto go test ./internal/genai -run '^(TestGenAINormalizerDropsSpansWithoutOperationName|TestGenAINormalizerDropsOnlySpansWithoutOperationName|TestGenAINormalizerClaimsKnownScopes)$' -count=1
GOTOOLCHAIN=auto go test ./internal/genai -count=1
```

Expected: PASS.

- [ ] **Step 5: Align GenAI documentation**

In `docs/harnesses/genai-scopes.md`, replace the statement that a matched-scope span without the operation key passes through with:

```markdown
A matched-scope span without `gen_ai.operation.name` drops from canonical
output: it cannot be classified as a GenAI operation or carry the required
canonical operation key. The edge does not infer an operation from an arbitrary
span name. Empty scope and resource groups are omitted. Every surviving span
receives the connector-written provenance keys below.
```

In `docs/design.md`, replace the GenAI claim/copy paragraphs that say the whole resource group and application scopes pass through with:

```markdown
The GenAI normalizer copies each claimed resource group before modification,
then retains only spans from matched GenAI scopes that carry
`gen_ai.operation.name`. Application spans on unmatched scopes and
unclassified matched-scope spans drop from canonical output; the raw pipeline
preserves them. Hierarchy, IDs, links, kind, and status of surviving spans pass
through without synthesized roots or replacement parents.
```

- [ ] **Step 6: Run conformance and commit**

```bash
cd connector/codingagentconnector
GOTOOLCHAIN=auto go test ./internal/genai ./... -count=1
cd ../..
git diff --check
git add connector/codingagentconnector/internal/genai/normalizer.go connector/codingagentconnector/internal/genai/normalizer_test.go docs/harnesses/genai-scopes.md docs/design.md
git commit -m "fix(genai): drop spans without operations"
```

Expected: all package and conformance tests PASS.

### Task 5: Rewrite stale component README behavior

**Files:**
- Modify: `connector/codingagentconnector/README.md`

**Interfaces:**
- Consumes: behavior established in Tasks 1-4 and the authoritative `docs/canonical-attributes.md` plus `docs/harnesses/` matrices.
- Produces: a component README with no removed vendor/turn attributes, correct Copilot mapping, and complete current configuration.

- [ ] **Step 1: Correct the Copilot overview and privacy statement**

Replace the Copilot bullet's vendor-extra sentence with:

```markdown
Per-request `github.copilot.cost` maps to `coding_agent.usage.cost` on chat
spans. Other vendor attributes drop under the closed canonical vocabulary.
```

Replace the sentence limiting content normalization to generated Codex spans with:

```markdown
Normalization never copies prompt text, tool arguments, or tool output into
canonical output on any edge. Raw vendor telemetry remains available only in
the separately configured raw pipelines.
```

- [ ] **Step 2: Replace the canonical attribute section**

Replace the root/chat/tool attribute tables with this current contract summary:

```markdown
Every emitted span carries `gen_ai.operation.name`, `coding_agent.source`,
and `coding_agent.client.name`. `coding_agent.client.version` is present when
the source reports a version. GenAI spans carry `coding_agent.source.scope`;
OpenHands roots carry it as well. Event-derived spans carry
`coding_agent.source.event`. Resources retain only the canonical identity
keys listed in [the canonical vocabulary](../../docs/canonical-attributes.md).

Roots use `gen_ai.operation.name=invoke_agent` and carry source-backed agent
and conversation identifiers. Logs-edge usage lives on chat spans, not as an
aggregate on the root. Native edges keep only source-backed canonical usage;
they do not invent totals. Logs-edge timeout finalization sets root Error
status. Close reasons and truncation are reported through connector self-metrics, not
`coding_agent.turn.*` span attributes.

Chat spans carry the request model when known, source-backed provider and
response metadata, canonical token counters, TTFT in seconds as a double, and
`coding_agent.usage.cost` only when the harness reports a per-request estimate.
Tool spans carry the tool name and call id when known. Tool failure maps to
OTel Error status; no vendor success boolean is emitted.

Codex `provider_name` is an operator-authored display label and drops;
`gen_ai.provider.name=openai` describes the wire protocol. Cursor billing
flags and vendor resource detail drop. Copilot's per-chat
`github.copilot.cost` maps to `coding_agent.usage.cost`; other
`github.copilot.*` attributes drop.

Span events are removed whole when they are known content events. Surviving
operational events keep only canonical attributes. Turn bookkeeping,
truncation flags, display labels, and unrestricted vendor namespaces are not
part of canonical output.
```

Keep [the canonical vocabulary](../../docs/canonical-attributes.md) and [the per-harness matrices](../../docs/harnesses/) as the complete key-level references.

- [ ] **Step 3: Replace the configuration table**

Use:

```markdown
| Setting | Default | Description |
| --- | --- | --- |
| `turn_timeout` | `10m` | Codex idle timeout without completion; Cursor maximum burst age measured from its first record. Timeout roots have Error status. |
| `reorder_window` | `30s` | Quiet period after the last event before a completed Codex turn or any Cursor burst finalizes. Must be less than `turn_timeout`; zero is allowed. |
| `max_active_turns` | `10000` | Bound on concurrently correlated Codex turns or Cursor bursts per edge; the least-recently-seen state emits before admitting a new one. |
| `max_events_per_turn` | `1000` | Bound on retained events and dedupe keys per turn/burst. Overflow is discarded without changing finalization state; truncation is reported by `otelcol_coding_agent_turns_truncated`. |
| `capture_identity` | `true` | Controls `coding_agent.user.id`, `coding_agent.user.email`, `coding_agent.team.id`, and resource `host.name`. Set `false` to suppress identity. |
| `signals` | `[logs, traces]` | Input signals this instance consumes. A disabled input edge emits nothing; pipeline wiring is the other way to select signals. |
```

Extend the example connector config with:

```yaml
    capture_identity: true
    signals: [logs, traces]
```

- [ ] **Step 4: Update delivery semantics**

Use this delivery paragraph:

```markdown
Redelivered retained records deduplicate while their Codex turn or Cursor
burst is open. Once the retained-event bound is reached, overflow records and
their dedupe keys are discarded without changing finalization state; overflow
redeliveries remain side-effect free. A record arriving after finalization can
start a new fragment trace. Trace and span IDs are deterministic for a full
replay, but the connector does not deduplicate separate exports or across
logs/traces edges.
```

- [ ] **Step 5: Check for stale claims**

Run:

```bash
! rg -n 'coding_agent\.turn\.|coding_agent\.model_provider|coding_agent\.cursor|coding_agent\.tool\.success|github\.copilot\.\*.*pass|vendor extras.*pass through' connector/codingagentconnector/README.md
rg -n 'capture_identity|signals|max_events_per_turn|coding_agent\.usage\.cost|self-metrics' connector/codingagentconnector/README.md
git diff --check -- connector/codingagentconnector/README.md
```

Expected: the negated stale-claim search succeeds with no matches; current settings and behavior are present; whitespace check passes.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/README.md
git commit -m "docs(connector): align canonical behavior"
```

### Task 6: Correct HA recovery and current-state docs

**Files:**
- Modify: `examples/k8s-ha/README.md`
- Modify: `docs/multi-instance-ha.md`

**Interfaces:**
- Consumes: actual `examples/k8s-ha/tier2-config.yaml`, which exports canonical traces only, and `examples/otelcol-s3.yaml`, which demonstrates raw preservation.
- Produces: recovery documentation conditional on an explicitly configured raw-preservation branch; no YAML or routing changes.

- [ ] **Step 1: Correct the Kubernetes example caveats**

Replace the shutdown and recovery bullets in `examples/k8s-ha/README.md` with:

```markdown
- A rolling restart of a tier-2 replica drains its active turns immediately;
  the `otelcol_coding_agent_turns_emitted` self-metric records these with
  `finish_reason=shutdown`. This is a metric label, not a canonical span
  attribute.
- This minimal example exports canonical traces only; it has no raw log or
  raw trace exporter and cannot replay source telemetry by itself. Raw replay
  requires adding a separate raw-preservation branch before the connector.
  `examples/otelcol-s3.yaml` shows that pattern. With raw telemetry preserved,
  replaying the complete event set through one instance rebuilds deterministic
  canonical IDs, and downstream consumers can dedupe on trace ID.
```

- [ ] **Step 2: Correct the HA current-state section**

Replace `docs/multi-instance-ha.md`'s stale current-state artifact bullets with:

```markdown
The bundled `collector-config.yaml` and `examples/otelcol-s3.yaml` run one
Collector per deployment. Multi-instance artifacts now exist in two forms:
`compose.e2e-routing.yaml` proves the two-tier consistent-hash topology
locally, and `examples/k8s-ha/` provides the Kubernetes tier-1 gateway and
tier-2 Collector manifests with the k8s resolver and RBAC. The Kubernetes
example exports canonical traces only; raw preservation is a separate
configuration choice shown by `examples/otelcol-s3.yaml`.
```

- [ ] **Step 3: Make general raw recovery conditional**

Replace the recovery section in `docs/multi-instance-ha.md` with:

```markdown
## Recovery property worth keeping in mind

When a deployment configures raw-preservation branches, source logs and traces
remain available independently of canonical output. Because trace IDs are
deterministic given the complete event set, canonical output can then be
rebuilt after topology churn or an incident by replaying raw logs through a
single instance. Downstream consumers dedupe on trace ID. The S3 example shows
the required raw branches; the minimal Kubernetes HA example does not include
them, so this recovery path is unavailable there unless the operator adds raw
exporters and storage.
```

- [ ] **Step 4: Verify the documentation claims**

Run:

```bash
! rg -n 'No Kubernetes manifests exist|Every artifact in the repository assumes one instance' docs/multi-instance-ha.md
rg -n 'canonical traces only|no raw log|raw-preservation|metric label|minimal Kubernetes HA example does not' examples/k8s-ha/README.md docs/multi-instance-ha.md
rg -n 'exporters:|awss3|otlp' examples/k8s-ha/tier2-config.yaml
git diff --check -- examples/k8s-ha/README.md docs/multi-instance-ha.md
```

Expected: stale absence claims are gone; recovery explicitly requires raw preservation; the tier-2 config inspection confirms why that precondition is necessary. No Compose or Kubernetes validation is needed because no YAML changes.

- [ ] **Step 5: Commit**

```bash
git add examples/k8s-ha/README.md docs/multi-instance-ha.md
git commit -m "docs(ha): correct raw replay requirements"
```

### Task 7: Run full and live verification

**Files:**
- Verify only; do not modify implementation or docs unless a verification failure identifies a concrete regression.

**Interfaces:**
- Consumes: all commits from Tasks 1-6.
- Produces: recorded unpaid and live verification results suitable for the PR. No commit is required for this task.

- [ ] **Step 1: Confirm the change set and working tree**

```bash
git status --short
git log --oneline -8
git diff --check HEAD~6..HEAD
```

Expected: only the six scoped commits are new; `.claude/` remains untracked and untouched; no unrelated files enter the commits.

- [ ] **Step 2: Run every unpaid check**

From the repository root:

```bash
GOTOOLCHAIN=auto ./scripts/check.sh
```

Expected: final output `ALL CHECKS PASSED`. This includes gofmt, shell checks, pinned lint, generated-file freshness, vet, unit/race tests in both modules, Collector build/config validation, Compose checks, image builds, and GoReleaser validation.

- [ ] **Step 3: Run every affected live harness once**

Because shared normalizers and canonical behavior changed, repository policy requires every live stack before opening a PR. Ensure the documented credentials are already present without printing them, then run each command once from the repository root:

```bash
GOTOOLCHAIN=auto ./scripts/e2e.sh
GOTOOLCHAIN=auto ./scripts/e2e-claude.sh
GOTOOLCHAIN=auto ./scripts/e2e-openai.sh
GOTOOLCHAIN=auto ./scripts/e2e-strands.sh
GOTOOLCHAIN=auto ./scripts/e2e-opencode.sh
GOTOOLCHAIN=auto ./scripts/e2e-pi.sh
GOTOOLCHAIN=auto ./scripts/e2e-copilot.sh
GOTOOLCHAIN=auto ./scripts/e2e-openhands.sh
```

Expected: each stack's validator passes. Do not loop paid runs. If a stack fails, record the failing harness and validator output, fix the concrete regression through a new TDD task, then rerun only the affected stack plus `./scripts/check.sh`. Cursor has no live stack; its fixture and race tests already ran in Step 2.

- [ ] **Step 4: Review final documentation and diff**

```bash
! rg -n 'coding_agent\.turn\.|coding_agent\.model_provider|coding_agent\.cursor|coding_agent\.tool\.success' connector/codingagentconnector/README.md
! rg -n 'fingerprint set bounds|id set bounds|passes through with content stripped|No Kubernetes manifests exist' docs connector/codingagentconnector/README.md
git diff --stat HEAD~6..HEAD
git status --short
```

Expected: stale claims are absent, the six commits contain only scoped changes, and no generated e2e output or credentials enter Git. Any remaining unrelated working-tree state stays uncommitted.
