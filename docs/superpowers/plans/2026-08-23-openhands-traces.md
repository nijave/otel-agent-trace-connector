# OpenHands Native-Trace Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Normalize OpenHands SDK native OpenTelemetry traces (`lmnr.tracer`
scope) into canonical `invoke_agent openhands` traces with `chat` and
`execute_tool` children, via a new stateless normalizer in the traces edge.

**Architecture:** A new stateless `internal/openhands` package mirrors
`internal/opencode`: claiming by scope plus OpenHands marker spans, per-batch
rewrite preserving wire IDs, canonical rename/reparent, attribute allowlist.
The traces router gains one edge. Validation runs on committed fixtures
authored from the source-verified wire, an `e2e/validator` table case, and a
new opt-in paid Compose stack shaped like `e2e/pi`.

**Tech Stack:** Go, OpenTelemetry Collector `pdata`/`connector` APIs (pins
already in `connector/codingagentconnector/go.mod`), testify, Docker Compose.

**Spec:** `docs/superpowers/specs/2026-08-23-openhands-traces-design.md`

## Global Constraints

- Run connector tests from `connector/codingagentconnector`: `go test ./...`; race variant `go test -race ./...`.
- Run `./scripts/check.sh` from the repo root before pushing.
- No new Go dependencies. No new configuration fields, component types, or metrics.
- Canonical output copies fields by **allowlist only**: only attributes named in the mapping below may enter canonical output. Content-bearing keys (`gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions`, `gen_ai.tool.definitions`, `gen_ai.request.base_url`, every `lmnr.span.*` bookkeeping key except consumed association properties) never appear in output.
- OpenHands spans preserve their wire trace/span IDs; roots clear their parent span ID; kept children reparent under their trace group's root.
- Conventional-commit subjects, lowercase, imperative (`feat(openhands): ...`, `test(e2e): ...`, `docs: ...`).
- The Vale prose-lint hook runs on every docs edit; keep prose active-voiced.
- All fixture timestamps use fixed values so output stays byte-deterministic.

---

### Task 1: Claiming and canonical rewrite in `internal/openhands`

**Files:**
- Create: `connector/codingagentconnector/internal/openhands/normalizer.go`
- Test: `connector/codingagentconnector/internal/openhands/normalizer_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces (used by Tasks 2–4):
  - `func New(next consumer.Traces) connector.Traces`
  - `func ContainsOpenHandsSpans(resourceSpans ptrace.ResourceSpans) bool`
  - Behavior: claimed groups emit one `invoke_agent openhands` root per input trace ID, `chat` children from LLM-type spans, `execute_tool` children from TOOL-type spans; everything else drops.

- [ ] **Step 1: Write the failing tests**

Create `normalizer_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

var (
	traceA = mustTraceID("11111111111111111111111111111111")
	traceB = mustTraceID("22222222222222222222222222222222")
)

func mustTraceID(s string) pcommon.TraceID {
	raw, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	var id pcommon.TraceID
	copy(id[:], raw)
	return id
}

func mustSpanID(s string) pcommon.SpanID {
	raw, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	var id pcommon.SpanID
	copy(id[:], raw)
	return id
}

type spanSpec struct {
	name     string
	spanID   string
	parentID string
	start    time.Time
	end      time.Time
	spanType string
	attrs    map[string]any
}

const sessionID = "0f0e0d0c-1111-2222-3333-444455556666"

var baseTime = time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

func baseAttrs() map[string]any {
	return map[string]any{attrSessionID: sessionID}
}

func makeSpan(trace pcommon.TraceID, spec spanSpec) ptrace.Span {
	span := ptrace.NewSpan()
	span.SetTraceID(trace)
	span.SetSpanID(mustSpanID(spec.spanID))
	if spec.parentID != "" {
		span.SetParentSpanID(mustSpanID(spec.parentID))
	}
	span.SetKind(ptrace.SpanKindInternal)
	start := spec.start
	if start.IsZero() {
		start = baseTime
	}
	end := spec.end
	if end.IsZero() {
		end = start.Add(time.Second)
	}
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
	if spec.spanType != "" {
		span.Attributes().PutStr(attrSpanType, spec.spanType)
	}
	for k, v := range spec.attrs {
		putRaw(span.Attributes(), k, v)
	}
	return span
}

func putRaw(attrs pcommon.Map, key string, v any) {
	switch value := v.(type) {
	case string:
		attrs.PutStr(key, value)
	case int64:
		attrs.PutInt(key, value)
	case bool:
		attrs.PutBool(key, value)
	}
}

// makeTraces assembles one resource with one lmnr.tracer scope holding the
// given spans.
func makeTraces(spans ...ptrace.Span) ptrace.Traces {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "agent-server")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(scopeName)
	ss.Scope().SetVersion("0.7.56")
	for _, span := range spans {
		span.CopyTo(ss.Spans().AppendEmpty())
	}
	return traces
}

type sink struct {
	mu      sync.Mutex
	batches []ptrace.Traces
}

func (*sink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *sink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, traces)
	return nil
}

func normalizeOne(t *testing.T, traces ptrace.Traces) ptrace.Traces {
	t.Helper()
	s := &sink{}
	require.NoError(t, New(s).ConsumeTraces(context.Background(), traces))
	require.Len(t, s.batches, 1)
	return s.batches[0]
}

func allSpansOf(traces ptrace.Traces) []ptrace.Span {
	var out []ptrace.Span
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		ss := traces.ResourceSpans().At(i).ScopeSpans()
		for j := 0; j < ss.Len(); j++ {
			spans := ss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				out = append(out, spans.At(k))
			}
		}
	}
	return out
}

func findByName(t *testing.T, traces ptrace.Traces, name string) []ptrace.Span {
	t.Helper()
	var out []ptrace.Span
	for _, span := range allSpansOf(traces) {
		if span.Name() == name {
			out = append(out, span)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no span named %q", name)
	}
	return out
}

func attrString(span ptrace.Span, key string) string {
	value, ok := span.Attributes().Get(key)
	if !ok || value.Type() != pcommon.ValueTypeStr {
		return ""
	}
	return value.Str()
}

func intValue(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Int()
}

func boolValue(span ptrace.Span, key string) bool {
	value, _ := span.Attributes().Get(key)
	return value.Bool()
}

func marshalT(t *testing.T, traces ptrace.Traces) string {
	t.Helper()
	data, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	return data
}

func conversationSpec() spanSpec {
	return spanSpec{name: "conversation", spanID: "aaaaaaaaaaaaaaaa", attrs: baseAttrs()}
}

func TestClaimsMarkerGroupsOnly(t *testing.T) {
	s := &sink{}
	require.NoError(t, New(s).ConsumeTraces(context.Background(),
		makeTraces(makeSpan(traceA, conversationSpec()))))
	require.Len(t, s.batches, 1)

	unmarked := makeTraces(makeSpan(traceA, spanSpec{name: "my_function", spanID: "bbbbbbbbbbbbbbbb"}))
	s2 := &sink{}
	require.NoError(t, New(s2).ConsumeTraces(context.Background(), unmarked))
	require.Empty(t, s2.batches)

	foreign := func() ptrace.Traces {
		traces := ptrace.NewTraces()
		rs := traces.ResourceSpans().AppendEmpty()
		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName("some.other.scope")
		makeSpan(traceA, conversationSpec()).CopyTo(ss.Spans().AppendEmpty())
		return traces
	}()
	s3 := &sink{}
	require.NoError(t, New(s3).ConsumeTraces(context.Background(), foreign))
	require.Empty(t, s3.batches)
}

func TestDelegateFlagAloneClaims(t *testing.T) {
	// No conversation- or agent-family span anywhere: only the delegate
	// metadata flag marks this group as OpenHands'.
	attrs := map[string]any{
		attrSessionID:  sessionID,
		attrIsDelegate: "true",
	}
	delegated := makeTraces(makeSpan(traceB, spanSpec{
		name: "litellm.completion", spanID: "cccccccccccccccc", spanType: "LLM", attrs: attrs,
	}))
	out := normalizeOne(t, delegated)
	require.Len(t, findByName(t, out, "invoke_agent openhands"), 1)
}

func TestConversationBecomesRootWithCanonicalAttrs(t *testing.T) {
	attrs := baseAttrs()
	attrs[attrUserID] = "42"
	attrs["conversation.tags.team"] = "core"
	traces := makeTraces(
		makeSpan(traceA, conversationSpec()),
		makeSpan(traceA, spanSpec{
			name: "agent.step", spanID: "dddddddddddddddd", parentID: "aaaaaaaaaaaaaaaa",
			start: baseTime.Add(-30 * time.Second),
		}),
	)
	out := normalizeOne(t, traces)
	roots := findByName(t, out, "invoke_agent openhands")
	require.Len(t, roots, 1)
	r := roots[0]
	require.Equal(t, traceA, r.TraceID())
	require.Equal(t, mustSpanID("aaaaaaaaaaaaaaaa"), r.SpanID())
	require.Equal(t, pcommon.SpanID{}, r.ParentSpanID())
	require.Equal(t, "invoke_agent", attrString(r, "gen_ai.operation.name"))
	require.Equal(t, "openhands", attrString(r, "gen_ai.agent.name"))
	require.Equal(t, sessionID, attrString(r, "gen_ai.conversation.id"))
	require.Equal(t, "native", attrString(r, "telemetry.source"))
	require.Equal(t, "openhands", attrString(r, "coding_agent.client.name"))
	require.Equal(t, scopeName, attrString(r, "coding_agent.source.scope"))
	require.Equal(t, "42", attrString(r, "enduser.pseudo.id"))
	require.Equal(t, "core", attrString(r, "coding_agent.openhands.tag.team"))
	// The dropped agent.step timing folds into the root bounds; the
	// conversation span ends one second after baseTime.
	require.Equal(t, baseTime.Add(-30*time.Second), r.StartTimestamp().AsTime())
	require.Equal(t, baseTime.Add(time.Second), r.EndTimestamp().AsTime())

	// Structural intermediates drop entirely: only root remains here.
	require.Len(t, allSpansOf(out), 1)
}

func TestLLMAndToolChildrenReparentAndRename(t *testing.T) {
	traces := makeTraces(
		makeSpan(traceA, conversationSpec()),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", parentID: "00000000000000dd",
			spanType: "LLM",
			attrs: map[string]any{
				"gen_ai.request.model":                     "anthropic/claude-sonnet-4-5",
				"gen_ai.usage.input_tokens":                int64(100),
				"gen_ai.usage.output_tokens":               int64(200),
				"gen_ai.usage.cache_read_input_tokens":     int64(50),
				"gen_ai.usage.cache_creation_input_tokens": int64(10),
				"llm.usage.total_tokens":                   int64(360),
				"gen_ai.input.messages":                    `[{"role":"user","content":"secret prompt"}]`,
				"lmnr.span.input":                          `"should not propagate"`,
			},
		}),
		makeSpan(traceA, spanSpec{
			name: "bash", spanID: "ffffffffffffff01", parentID: "00000000000000dd",
			spanType: "TOOL",
			attrs:    map[string]any{attrToolCallID: "call-1"},
		}),
		makeSpan(traceA, spanSpec{
			name: "bash", spanID: "ffffffffffffff02",
			spanType: "TOOL",
			attrs:    map[string]any{attrToolCallID: "call-1"},
		}),
	)
	out := normalizeOne(t, traces)
	root := findByName(t, out, "invoke_agent openhands")[0]

	chats := findByName(t, out, "chat anthropic/claude-sonnet-4-5")
	require.Len(t, chats, 1)
	chat := chats[0]
	require.Equal(t, root.TraceID(), chat.TraceID())
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, "chat", attrString(chat, "gen_ai.operation.name"))
	require.Equal(t, "anthropic/claude-sonnet-4-5", attrString(chat, "gen_ai.request.model"))
	require.Equal(t, int64(100), intValue(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(200), intValue(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(50), intValue(t, chat, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(10), intValue(t, chat, "gen_ai.usage.cache_creation.input_tokens"))
	_, ok := chat.Attributes().Get("llm.usage.total_tokens")
	require.False(t, ok)
	_, ok = chat.Attributes().Get("gen_ai.input.messages")
	require.False(t, ok)
	_, ok = chat.Attributes().Get("lmnr.span.input")
	require.False(t, ok)

	tools := findByName(t, out, "execute_tool bash")
	require.Len(t, tools, 1) // deduped on tool_call_id
	require.Equal(t, root.SpanID(), tools[0].ParentSpanID())
	require.Equal(t, "execute_tool", attrString(tools[0], "gen_ai.operation.name"))
	require.Equal(t, "bash", attrString(tools[0], "gen_ai.tool.name"))

	// Root + chat + one deduped execute_tool.
	require.Len(t, allSpansOf(out), 3)
}

func TestStreamedCompletionKeepsBareChatWithoutUsage(t *testing.T) {
	traces := makeTraces(
		makeSpan(traceA, conversationSpec()),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee",
			spanType: "LLM",
			attrs:    map[string]any{"gen_ai.response.model": "gpt-5.2"},
		}),
	)
	out := normalizeOne(t, traces)
	chats := findByName(t, out, "chat")
	require.Len(t, chats, 1)
	_, ok := chats[0].Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, ok)
}

func TestFragmentWithoutConversationGetsSyntheticRoot(t *testing.T) {
	// Mid-conversation exports contain marker spans (agent.step) but not yet
	// the long-lived conversation root, which only ends with the
	// conversation itself.
	traces := makeTraces(
		makeSpan(traceA, spanSpec{
			name: "agent.step", spanID: "dddddddddddddddd",
		}),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee",
			spanType: "LLM",
			attrs: map[string]any{
				attrSessionID:              sessionID,
				"gen_ai.request.model":     "m",
				"gen_ai.usage.input_tokens": int64(1),
			},
		}),
	)
	out := normalizeOne(t, traces)
	roots := findByName(t, out, "invoke_agent openhands")
	require.Len(t, roots, 1)
	require.Equal(t, traceA, roots[0].TraceID())
	sum := sha256.Sum256(append(append([]byte{}, traceA[:]...), []byte(syntheticRootDiscriminator)...))
	var want pcommon.SpanID
	copy(want[:], sum[:8])
	require.Equal(t, want, roots[0].SpanID())
	// The synthetic root inherits the conversation id from a child span.
	require.Equal(t, sessionID, attrString(roots[0], "gen_ai.conversation.id"))
}

func TestDelegateSiblingCarriesLinkage(t *testing.T) {
	attrs := baseAttrs()
	attrs[attrIsDelegate] = "true"
	attrs[attrMetadata+"task_id"] = "task-9"
	attrs[attrMetadata+"parent_session_id"] = "parent-conversation-uuid"
	delegated := makeTraces(makeSpan(traceB, spanSpec{
		name: "conversation", spanID: "cccccccccccccccc", attrs: attrs,
	}))
	out := normalizeOne(t, delegated)
	root := findByName(t, out, "invoke_agent openhands")[0]
	require.True(t, boolValue(root, delegatePrefix+"delegate"))
	require.Equal(t, "task-9", attrString(root, delegatePrefix+"task_id"))
	require.Equal(t, "parent-conversation-uuid", attrString(root, delegatePrefix+"parent_session_id"))
}

func TestTagsAssociationPropertyCopied(t *testing.T) {
	attrs := baseAttrs()
	attrs[attrTags] = []string{"delegate"}
	delegated := makeTraces(makeSpan(traceB, spanSpec{
		name: "conversation", spanID: "cccccccccccccccc", attrs: attrs,
	}))
	out := normalizeOne(t, delegated)
	root := findByName(t, out, "invoke_agent openhands")[0]
	tags, ok := root.Attributes().Get("coding_agent.openhands.tags")
	require.True(t, ok)
	require.Equal(t, 1, tags.Slice().Len())
	require.Equal(t, "delegate", tags.Slice().At(0).Str())
}

func TestOutputOrderStableUnderShuffledInput(t *testing.T) {
	specs := []spanSpec{
		conversationSpec(),
		{name: "agent.step", spanID: "dddddddddddddddd", parentID: "aaaaaaaaaaaaaaaa"},
		{name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", parentID: "dddddddddddddddd", spanType: "LLM",
			attrs: map[string]any{"gen_ai.request.model": "m", "gen_ai.usage.input_tokens": int64(3)}},
		{name: "read_file", spanID: "f100000000000001", parentID: "dddddddddddddddd", spanType: "TOOL",
			attrs: map[string]any{attrToolCallID: "c1"}},
	}
	build := func(order []int) ptrace.Traces {
		var built []ptrace.Span
		for _, idx := range order {
			built = append(built, makeSpan(traceA, specs[idx]))
		}
		return makeTraces(built...)
	}
	forward := normalizeOne(t, build([]int{0, 1, 2, 3}))
	reversed := normalizeOne(t, build([]int{3, 2, 1, 0}))
	require.JSONEq(t, marshalT(t, forward), marshalT(t, reversed))
}

func TestNoLaminarBookkeepingInOutput(t *testing.T) {
	out := normalizeOne(t, makeTraces(
		makeSpan(traceA, conversationSpec()),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", spanType: "LLM",
			attrs: map[string]any{
				"lmnr.span.path":        `["conversation"]`,
				"lmnr.span.sdk_version": "0.7.56",
			},
		}),
	))
	for _, span := range allSpansOf(out) {
		span.Attributes().Range(func(k string, _ pcommon.Value) bool {
			if strings.HasPrefix(k, "lmnr.span.") {
				t.Errorf("bookkeeping attribute %q leaked onto %s", k, span.Name())
			}
			return true
		})
	}
}
```

Note: `TestConversationBecomesRootWithCanonicalAttrs` sets the agent.step
start 30 seconds BEFORE baseTime, so the root bounds check proves folding.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/openhands/ -v`
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Write `normalizer.go`**

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"crypto/sha256"
	"sort"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	scopeName  = "lmnr.tracer"
	clientName = "openhands"
	agentName  = "openhands"

	wireConversation = "conversation"

	attrSpanType   = "lmnr.span.type"
	attrSessionID  = "lmnr.association.properties.session_id"
	attrUserID     = "lmnr.association.properties.user_id"
	attrTags       = "lmnr.association.properties.tags"
	attrMetadata   = "lmnr.association.properties.metadata."
	attrToolCallID = attrMetadata + "tool_call_id"
	attrIsDelegate = attrMetadata + "is_delegate"

	tagPrefix = "conversation.tags."

	openhandsTagPrefix = "coding_agent.openhands.tag."
	openhandsTagsAttr  = "coding_agent.openhands.tags"
	delegatePrefix     = "coding_agent.openhands.delegate."
	delegateFlag       = "coding_agent.openhands.delegate"

	syntheticRootDiscriminator = ":synthetic-root"
)

// delegateKeys are the linkage metadata attributes copied onto delegate
// roots so severed sibling fragments stay reconcilable downstream.
var delegateKeys = [][2]string{
	{attrMetadata + "task_id", delegatePrefix + "task_id"},
	{attrMetadata + "subagent_type", delegatePrefix + "subagent_type"},
	{attrMetadata + "parent_session_id", delegatePrefix + "parent_session_id"},
	{attrToolCallID, delegatePrefix + "tool_call_id"},
}

// usageKeys remap the Laminar/LiteLLM accounting keys onto the canonical
// namespace. llm.usage.total_tokens is derivable and never copied.
var usageKeys = [][2]string{
	{"gen_ai.usage.input_tokens", "gen_ai.usage.input_tokens"},
	{"gen_ai.usage.output_tokens", "gen_ai.usage.output_tokens"},
	{"gen_ai.usage.cache_read_input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"gen_ai.usage.cache_creation_input_tokens", "gen_ai.usage.cache_creation.input_tokens"},
}

// markerSpanNames are the conversation- and agent-family names only the
// OpenHands SDK emits. Scope lmnr.tracer is shared by every
// Laminar-instrumented application, so claiming needs one of these markers
// (or the delegate flag) before a group belongs to this edge.
var markerSpanNames = map[string]bool{
	"conversation":                true,
	"conversation.send_message":   true,
	"conversation.run":            true,
	"conversation.arun":           true,
	"conversation.ask_agent":      true,
	"conversation.generate_title": true,
	"agent.step":                  true,
	"agent.astep":                 true,
	"acp_agent.step":              true,
	"acp_agent.astep":             true,
}

type role int

const (
	roleDrop role = iota
	roleRoot
	roleChat
	roleTool
)

// openhandsTraceNormalizer rewrites OpenHands SDK spans into the canonical
// vocabulary. It is stateless: mid-conversation exports arrive without the
// long-lived conversation root, so each batch is rewritten as-is and
// backends reassemble by the preserved IDs.
type openhandsTraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless OpenHands native traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &openhandsTraceNormalizer{next: next}
}

func (*openhandsTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *openhandsTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputRS := input.ResourceSpans().At(i)
		groups, order := collect(inputRS)
		if len(groups) == 0 {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputRS.Resource().CopyTo(rs.Resource())
		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName(scopeName)
		for _, key := range order {
			emitGroup(ss.Spans(), groups[key])
		}
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsOpenHandsSpans reports whether any lmnr.tracer scope in the group
// carries an explicit OpenHands marker, keeping generic
// Laminar-instrumented applications unclaimed.
func ContainsOpenHandsSpans(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		ss := resourceSpans.ScopeSpans().At(i)
		if ss.Scope().Name() != scopeName {
			continue
		}
		for j := 0; j < ss.Spans().Len(); j++ {
			span := ss.Spans().At(j)
			if markerSpanNames[span.Name()] {
				return true
			}
			if firstString(span.Attributes(), attrIsDelegate) == "true" {
				return true
			}
		}
	}
	return false
}

// classify maps a wire span to its canonical role via the Laminar span
// type, falling back to the conversation-name check for roots.
func classify(span ptrace.Span) role {
	switch firstString(span.Attributes(), attrSpanType) {
	case "LLM":
		return roleChat
	case "TOOL":
		return roleTool
	}
	if span.Name() == wireConversation {
		return roleRoot
	}
	return roleDrop
}

type kept struct {
	span ptrace.Span
	rol  role
}

type traceGroup struct {
	traceID   pcommon.TraceID
	root      *ptrace.Span
	children  []kept
	seenTools map[string]bool
	minStart  pcommon.Timestamp
	maxEnd    pcommon.Timestamp
}

// collect buckets claimed spans by trace ID. Groups order deterministically
// by earliest start then trace-ID bytes, so shuffled input yields identical
// output ordering.
func collect(rs ptrace.ResourceSpans) (map[pcommon.TraceID]*traceGroup, []pcommon.TraceID) {
	groups := map[pcommon.TraceID]*traceGroup{}
	for i := 0; i < rs.ScopeSpans().Len(); i++ {
		ss := rs.ScopeSpans().At(i)
		if ss.Scope().Name() != scopeName {
			continue
		}
		for j := 0; j < ss.Spans().Len(); j++ {
			span := ss.Spans().At(j)
			rol := classify(span)
			if rol == roleDrop {
				continue
			}
			key := span.TraceID()
			g := groups[key]
			if g == nil {
				g = &traceGroup{traceID: key, seenTools: map[string]bool{}}
				groups[key] = g
			}
			g.minStart = minTime(g.minStart, span.StartTimestamp())
			g.maxEnd = maxTime(g.maxEnd, span.EndTimestamp())
			switch rol {
			case roleRoot:
				if g.root == nil || span.StartTimestamp() < g.root.StartTimestamp() {
					root := span
					g.root = &root
				}
			case roleTool:
				id := firstString(span.Attributes(), attrToolCallID)
				if id != "" {
					if g.seenTools[id] {
						continue
					}
					g.seenTools[id] = true
				}
				g.children = append(g.children, kept{span: span, rol: rol})
			default:
				g.children = append(g.children, kept{span: span, rol: rol})
			}
		}
	}
	order := make([]pcommon.TraceID, 0, len(groups))
	for id := range groups {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		mi, mj := groups[order[i]], groups[order[j]]
		if mi.minStart != mj.minStart {
			return mi.minStart < mj.minStart
		}
		return string(mi.traceID[:]) < string(mj.traceID[:])
	})
	return groups, order
}

// emitGroup writes one canonical trace: the invoke_agent root followed by
// its reparented chat and execute_tool children in deterministic order.
func emitGroup(dst ptrace.SpanSlice, g *traceGroup) {
	root := dst.AppendEmpty()
	if g.root != nil {
		copySpanMetadata(*g.root, root)
	} else {
		root.SetTraceID(g.traceID)
		root.SetKind(ptrace.SpanKindInternal)
	}
	root.SetSpanID(rootSpanID(g))
	root.SetParentSpanID(pcommon.SpanID{})
	root.SetName("invoke_agent " + agentName)
	root.SetStartTimestamp(g.minStart)
	root.SetEndTimestamp(g.maxEnd)
	putRootAttributes(root.Attributes(), g)

	children := append([]kept(nil), g.children...)
	sort.Slice(children, func(i, j int) bool {
		si, sj := children[i].span, children[j].span
		if si.StartTimestamp() != sj.StartTimestamp() {
			return si.StartTimestamp() < sj.StartTimestamp()
		}
		return string(si.SpanID()[:]) < string(sj.SpanID()[:])
	})
	for _, child := range children {
		span := dst.AppendEmpty()
		copySpanMetadata(child.span, span)
		span.SetParentSpanID(root.SpanID())
		switch child.rol {
		case roleChat:
			normalizeChat(child.span, span)
		case roleTool:
			normalizeTool(child.span, span)
		}
	}
}

// rootSpanID keeps the conversation span's ID when present; fragment groups
// exported before their root ends get a derived stable ID that cannot
// collide with the SDK's random IDs.
func rootSpanID(g *traceGroup) pcommon.SpanID {
	if g.root != nil {
		return g.root.SpanID()
	}
	h := sha256.New()
	_, _ = h.Write(g.traceID[:])
	_, _ = h.Write([]byte(syntheticRootDiscriminator))
	var id pcommon.SpanID
	copy(id[:], h.Sum(nil)[:8])
	return id
}

func putRootAttributes(attrs pcommon.Map, g *traceGroup) {
	// Root attributes read from the conversation span when present.
	// Fragment groups exported before their root ends inherit the
	// conversation-level attributes from their kept children instead —
	// every OpenHands span carries the association properties.
	src := pcommon.Map{}
	if g.root != nil {
		src = (*g.root).Attributes()
	} else {
		src = pcommon.NewMap()
		for _, k := range g.children {
			k.span.Attributes().Range(func(name string, v pcommon.Value) bool {
				if _, exists := src.Get(name); !exists &&
					(strings.HasPrefix(name, attrMetadata) ||
						strings.HasPrefix(name, "lmnr.association.properties.") ||
						strings.HasPrefix(name, tagPrefix)) {
					v.CopyTo(src.PutEmpty(name))
				}
				return true
			})
		}
	}
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", agentName)
	if sid := firstString(src, attrSessionID); sid != "" {
		attrs.PutStr("gen_ai.conversation.id", sid)
	}
	attrs.PutStr("telemetry.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("coding_agent.source.scope", scopeName)
	if uid := firstString(src, attrUserID); uid != "" {
		attrs.PutStr("enduser.pseudo.id", uid)
	}
	if tags, ok := src.Get(attrTags); ok && tags.Type() == pcommon.ValueTypeSlice {
		dst := attrs.PutEmptySlice(openhandsTagsAttr)
		for i := 0; i < tags.Slice().Len(); i++ {
			if v := tags.Slice().At(i); v.Type() == pcommon.ValueTypeStr {
				dst.AppendEmpty().SetStr(v.Str())
			}
		}
	}
	keyedTags := map[string]string{}
	src.Range(func(k string, v pcommon.Value) bool {
		if strings.HasPrefix(k, tagPrefix) && v.Type() == pcommon.ValueTypeStr {
			keyedTags[strings.TrimPrefix(k, tagPrefix)] = v.Str()
		}
		return true
	})
	for k, v := range keyedTags {
		attrs.PutStr(openhandsTagPrefix+k, v)
	}
	if firstString(src, attrIsDelegate) == "true" {
		attrs.PutBool(delegateFlag, true)
	}
	for _, pair := range delegateKeys {
		if v := firstString(src, pair[0]); v != "" {
			attrs.PutStr(pair[1], v)
		}
	}
}

func normalizeChat(wire, span ptrace.Span) {
	attrs := span.Attributes()
	attrs.PutStr("telemetry.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("gen_ai.operation.name", "chat")
	name := "chat"
	if model := firstString(wire.Attributes(), "gen_ai.request.model"); model != "" {
		attrs.PutStr("gen_ai.request.model", model)
		name += " " + model
	}
	span.SetName(name)
	for _, pair := range usageKeys {
		if v, ok := wire.Attributes().Get(pair[0]); ok && v.Type() == pcommon.ValueTypeInt {
			attrs.PutInt(pair[1], v.Int())
		}
	}
}

func normalizeTool(wire, span ptrace.Span) {
	attrs := span.Attributes()
	attrs.PutStr("telemetry.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("gen_ai.operation.name", "execute_tool")
	tool := wire.Name()
	attrs.PutStr("gen_ai.tool.name", tool)
	span.SetName("execute_tool " + tool)
}

func copySpanMetadata(wire, span ptrace.Span) {
	span.SetTraceID(wire.TraceID())
	span.SetSpanID(wire.SpanID())
	span.SetParentSpanID(wire.ParentSpanID())
	span.SetKind(wire.Kind())
	span.SetStartTimestamp(wire.StartTimestamp())
	span.SetEndTimestamp(wire.EndTimestamp())
	span.SetFlags(wire.Flags())
	span.SetDroppedAttributesCount(wire.DroppedAttributesCount())
	status := wire.Status()
	span.Status().SetCode(status.Code())
	span.Status().SetMessage(status.Message())
}

func firstString(attrs pcommon.Map, key string) string {
	value, ok := attrs.Get(key)
	if !ok || value.Type() != pcommon.ValueTypeStr {
		return ""
	}
	return value.Str()
}

func minTime(a, b pcommon.Timestamp) pcommon.Timestamp {
	if a == 0 || b < a {
		return b
	}
	return a
}

func maxTime(a, b pcommon.Timestamp) pcommon.Timestamp {
	if b > a {
		return b
	}
	return a
}

var _ connector.Traces = (*openhandsTraceNormalizer)(nil)
```

Implementation notes:
- `emitGroup` calls `root.SetSpanID(rootSpanID(g))` AFTER `copySpanMetadata`
  so the synthetic-root derivation applies in both branches.
- The loop variable capture in `collect`'s root branch relies on Go 1.22+
  per-iteration variables; if the pinned toolchain predates 1.22, copy the
  span to a fresh local before taking its address.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test ./internal/openhands/ -v`
Expected: PASS for all tests.

- [ ] **Step 5: Race test**

Run: `cd connector/codingagentconnector && go test -race ./internal/openhands/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/internal/openhands/
git commit -m "feat(openhands): claim lmnr.tracer groups into canonical traces"
```

---

### Task 2: Wire the OpenHands edge into the traces router

**Files:**
- Modify: `connector/codingagentconnector/traces.go` (edges list at line 32 gains `openhands.New(next)`; doc comment updated)
- Modify: `connector/codingagentconnector/traces_test.go` (append one test)

**Interfaces:**
- Consumes: `openhands.New(next consumer.Traces) connector.Traces` from Task 1.
- Produces: routed behavior only; no new symbols.

- [ ] **Step 1: Write the failing test**

Append to `traces_test.go`. Reuse the file's existing sink type and helpers;
if it names them differently than shown, adapt names, keep assertions:

```go
func TestTracesRouterClaimsOpenHandsGroup(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "agent-server")

	lmnr := rs.ScopeSpans().AppendEmpty()
	lmnr.Scope().SetName("lmnr.tracer")
	root := lmnr.Spans().AppendEmpty()
	root.SetTraceID(pcommon.TraceID([16]byte{1}))
	root.SetSpanID(pcommon.SpanID([8]byte{2}))
	root.SetName("conversation")
	root.Attributes().PutStr("lmnr.association.properties.session_id", "conv-uuid")
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 1, 0, time.UTC)))

	next := &traceSink{}
	router := newTracesRouter(next)
	require.NoError(t, router.ConsumeTraces(context.Background(), traces))

	require.Len(t, next.traces, 1)
	var names []string
	spans := next.traces[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for i := 0; i < spans.Len(); i++ {
		names = append(names, spans.At(i).Name())
	}
	require.Contains(t, names, "invoke_agent openhands")
	require.NotContains(t, names, "unrelated_span")
}
```

If `traces_test.go` lacks a sink helper, add the same minimal
`Capabilities`+`ConsumeTraces` sink pattern used in `logs_test.go`.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd connector/codingagentconnector && go test . -run TestTracesRouterClaimsOpenHandsGroup -v`
Expected: FAIL — no `invoke_agent openhands` in output (edge not wired).

- [ ] **Step 3: Add the import and edge**

In `traces.go`, add to imports:

```go
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/openhands"
```

and extend the edges list:

```go
func newTracesRouter(next consumer.Traces) connector.Traces {
	return &tracesRouter{edges: []connector.Traces{claude.New(next), genai.New(next), opencode.New(next), pi.New(next), openhands.New(next)}}
}
```

Update the `tracesRouter` doc comment's disjointness sentence: the GenAI edge
defers to Claude and OpenCode, and the OpenHands edge claims `lmnr.tracer`
scope groups carrying OpenHands markers.

- [ ] **Step 4: Run the full connector suite with race**

Run: `cd connector/codingagentconnector && go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/traces.go connector/codingagentconnector/traces_test.go
git commit -m "feat: route lmnr.tracer groups through the openhands edge"
```

---

### Task 3: Wire-reference fixtures and replay test

**Files:**
- Create: `connector/codingagentconnector/internal/openhands/testdata/openhands-native-traces.json`
- Create: `connector/codingagentconnector/internal/openhands/testdata/openhands-canonical.otlp.json`
- Test: `connector/codingagentconnector/internal/openhands/fixtures_test.go`

**Interfaces:**
- Consumes: `New`, `normalizeOne`, `makeTraces`, `sink` from Task 1's package.
- Produces: the two fixture files; Task 4 reads `openhands-canonical.otlp.json` by relative path.

- [ ] **Step 1: Author the raw fixture**

`testdata/openhands-native-traces.json` — OTLP/JSON traces export
(`ptrace.JSONUnmarshaler` shape): one resource
(`service.name=agent-server`), one scope `lmnr.tracer`/`0.7.56`, two
trace-ID groups, lowercase hex IDs, attribute values wrapped as
`stringValue`/`intValue`/`boolValue`/`arrayValue`.
Fixed timestamps (nanoseconds since epoch):
`T0=1787440800000000000` (2026-08-23T10:00:00Z), `T1=T0+1000000000`,
`T2=T0+2000000000`, `T3=T0+3000000000`, `T4=T0+4000000000`, `T5=T0+5000000000`.
Follow the wrapper conventions of
`connector/codingagentconnector/internal/cursor/testdata/cursor-native-logs.json`.

Trace A, `traceId` `11111111111111111111111111111111` (main conversation):

| # | name | spanId | parent | start/end | notable attrs |
|---|------|--------|--------|-----------|---------------|
| 1 | `conversation` | `aaaaaaaaaaaaaaaa` | none | T0/T5 | `lmnr.span.type=DEFAULT`, session_id=`0f0e0d0c-1111-2222-3333-444455556666`, user_id=`42`, `conversation.tags.team=core` |
| 2 | `conversation.run` | `b100000000000000` | #1 | T0/T4 | `lmnr.span.type=DEFAULT` |
| 3 | `agent.step` | `b200000000000000` | #2 | T1/T4 | `lmnr.span.type=DEFAULT`, `lmnr.span.path=["conversation","run"]` |
| 4 | `litellm.completion` | `c100000000000000` | #3 | T1/T2 | `lmnr.span.type=LLM`, `gen_ai.request.model=anthropic/claude-sonnet-4-5`, usage 100/200/cache 50/10, `llm.usage.total_tokens=360`, `gen_ai.input.messages=[{"role":"user","content":"secret prompt"}]`, `lmnr.span.input="should not propagate"` |
| 5 | `bash` | `c200000000000000` | #3 | T2/T3 | `lmnr.span.type=TOOL`, metadata.tool_call_id=`call-1`, `gen_ai.tool.description=runs shell` |
| 6 | `bash` (result record) | `c300000000000000` | none | T3/T3 | `lmnr.span.type=TOOL`, metadata.tool_call_id=`call-1` |

Trace B, `traceId` `22222222222222222222222222222222` (delegate fragment):

| # | name | spanId | parent | start/end | notable attrs |
|---|------|--------|--------|-----------|---------------|
| 1 | `conversation` | `d100000000000000` | none | T2/T5 | session_id same UUID, `metadata.is_delegate=true`, `metadata.task_id=task-9`, `metadata.subagent_type=bash_delegate`, `metadata.parent_session_id=0f0e0d0c-1111-2222-3333-444455556666`, `lmnr.association.properties.tags=["delegate"]` |
| 2 | `litellm.responses` | `d200000000000000` | none | T3/T4 | `lmnr.span.type=LLM`, no model, usage input=7/output=9 only |

Every span also carries `lmnr.span.instrumentation_source="python"` and
`lmnr.span.sdk_version=0.7.56` (they must not leak; the fixture proves it).

- [ ] **Step 2: Write the replay test**

`fixtures_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func loadFixtureTraces(t *testing.T) ptrace.Traces {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "openhands-native-traces.json"))
	require.NoError(t, err)
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(raw)
	require.NoError(t, err)
	return traces
}

// replayFixture feeds the fixture through the edge once; the stateless edge
// emits a single batch holding both trace groups.
func replayFixture(t *testing.T) ptrace.Traces {
	t.Helper()
	s := &sink{}
	require.NoError(t, New(s).ConsumeTraces(context.Background(), loadFixtureTraces(t)))
	require.Len(t, s.batches, 1)
	return s.batches[0]
}

func TestFixtureReplayMatchesCanonicalFixture(t *testing.T) {
	actual, err := (&ptrace.JSONMarshaler{}).MarshalTraces(replayFixture(t))
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "openhands-canonical.otlp.json"))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), actual)
}

func TestFixtureReplayShuffleStable(t *testing.T) {
	plain := replayFixture(t)

	// Reverse span order within the scope and re-run through the edge.
	source := loadFixtureTraces(t)
	spans := source.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	var reversed []ptrace.Span
	for i := spans.Len() - 1; i >= 0; i-- {
		reversed = append(reversed, spans.At(i))
	}
	shuffled := normalizeOne(t, makeTraces(reversed...))

	a, err := (&ptrace.JSONMarshaler{}).MarshalTraces(plain)
	require.NoError(t, err)
	b, err := (&ptrace.JSONMarshaler{}).MarshalTraces(shuffled)
	require.NoError(t, err)
	require.JSONEq(t, string(a), string(b))
}

// Guard the fixture's own hygiene: the raw fixture really carries content
// keys, so the canonical comparison proves stripping rather than absence.
func TestRawFixtureCarriesContentThatOutputMustNot(t *testing.T) {
	spans := loadFixtureTraces(t).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	var sawContent bool
	for i := 0; i < spans.Len(); i++ {
		if _, ok := spans.At(i).Attributes().Get("gen_ai.input.messages"); ok {
			sawContent = true
		}
	}
	require.True(t, sawContent)
}

var _ = pcommon.Timestamp(0) // keep import if unused elsewhere
```

Delete the final `var _` line if `pcommon` ends up unused — prefer removing
the import instead.

- [ ] **Step 3: Generate the canonical fixture**

Run: `cd connector/codingagentconnector && go test ./internal/openhands/ -run TestFixtureReplayMatchesCanonicalFixture -v`
Expected: FAIL (canonical fixture missing). Write a one-off scratch test (do
not commit it) that dumps `(&ptrace.JSONMarshaler{}).MarshalTraces(replayFixture(t))`
to `testdata/openhands-canonical.otlp.json`, run it, delete the scratch
test. Eyeball the generated file against the spec: two `invoke_agent
openhands` roots sharing one `gen_ai.conversation.id`; trace-A root bounds
T0..T5 with `enduser.pseudo.id=42` and `coding_agent.openhands.tag.team=core`;
one `chat anthropic/claude-sonnet-4-5` with usage 100/200 and cache 50/10,
no `llm.usage.total_tokens`, no messages; one `execute_tool bash` (deduped);
trace-B root flagged `coding_agent.openhands.delegate=true` with task
linkage and `.tags=["delegate"]`; one bare `chat` with usage 7/9 mapped;
zero structural or bookkeeping spans anywhere.

- [ ] **Step 4: Run the fixture tests**

Run: `cd connector/codingagentconnector && go test ./internal/openhands/ -v`
Expected: PASS — replay matches the canonical fixture; shuffled order derives identical output.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/openhands/testdata/ connector/codingagentconnector/internal/openhands/fixtures_test.go
git commit -m "test(openhands): add wire-reference fixtures and replay test"
```

---

### Task 4: E2E validator assertions for the OpenHands shape

**Files:**
- Modify: `e2e/validator/validator.go` (validators near the Cursor helpers ~line 497; `validateCanonicalFile` switch ~line 16)
- Modify: `e2e/validator/live_test.go:29-36` (supported-agents switch; RAW_TRACE_FILE requirement; raw-check call)
- Test: `e2e/validator/validator_test.go` (repo-root module)

**Interfaces:**
- Consumes: `connector/codingagentconnector/internal/openhands/testdata/openhands-canonical.otlp.json` by relative path (the e2e module does not import the connector module).
- Produces: `validateOpenHandsCanonicalFile(path string) error`, `validateOpenHandsRawFile(path, runID string) error`, `case "openhands"` in both live-run dispatchers.

- [ ] **Step 1: Write the failing test**

Append to `validator_test.go` (match the file's existing imports; add `os`
and `ptrace` if absent):

```go
func TestOpenHandsCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "openhands", "testdata", "openhands-canonical.otlp.json")
	require.NoError(t, validateOpenHandsCanonicalFile(path))
}

func TestOpenHandsCanonicalFixtureRejectsContent(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "openhands", "testdata", "openhands-canonical.otlp.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(raw)
	require.NoError(t, err)
	span := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	span.Attributes().PutStr("gen_ai.input.messages", `[{"role":"user","content":"leak"}]`)

	temp := filepath.Join(t.TempDir(), "leaky.json")
	data, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(temp, data, 0o600))

	err = validateOpenHandsCanonicalFile(temp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sensitive")
}
```

Check what message `rejectSensitiveAttrs` actually returns; if it does not
contain "sensitive", assert on the real substring instead of weakening the
check.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./e2e/validator/ -run TestOpenHandsCanonical -v`
Expected: FAIL — `validateOpenHandsCanonicalFile` undefined.

- [ ] **Step 3: Add the validators**

In `validator.go`, next to the Cursor helpers (~line 497):

```go
// validateOpenHandsCanonicalFile asserts the canonical OpenHands shape over
// a committed fixture.
func validateOpenHandsCanonicalFile(path string) error {
	return validateTraceFile(path, "", validateOpenHandsCanonicalTraces)
}

// validateOpenHandsRawFile pins the raw wire shape the normalizer claims:
// marker spans and LLM spans present under the lmnr.tracer scope.
func validateOpenHandsRawFile(path, _ string) error {
	return validateTraceFile(path, "", validateOpenHandsRawTraces)
}

func validateOpenHandsCanonicalTraces(traces ptrace.Traces, _ string) error {
	spans := allSpans(traces)
	if err := validateOpenHandsSpans(spans); err != nil {
		return err
	}
	return rejectSensitiveAttrs(spans)
}

func validateOpenHandsRawTraces(traces ptrace.Traces, _ string) error {
	spans := allSpans(traces)
	var markers, llm int
	for _, span := range spans {
		switch span.Name() {
		case "conversation", "agent.step", "agent.astep":
			markers++
		case "litellm.completion", "litellm.responses":
			llm++
		}
	}
	if markers == 0 {
		return fmt.Errorf("no openhands marker spans in raw capture")
	}
	if llm == 0 {
		return fmt.Errorf("no llm spans in raw openhands capture")
	}
	return nil
}

func validateOpenHandsSpans(spans []ptrace.Span) error {
	var roots, others int
	for _, span := range spans {
		switch {
		case span.Name() == "invoke_agent openhands":
			roots++
			if got := stringAttr(span, "gen_ai.conversation.id"); got == "" {
				return fmt.Errorf("openhands root missing gen_ai.conversation.id")
			}
			if got := stringAttr(span, "gen_ai.agent.name"); got != "openhands" {
				return fmt.Errorf("openhands root agent name %q", got)
			}
		case strings.HasPrefix(span.Name(), "chat"):
			others++
			if got := stringAttr(span, "gen_ai.operation.name"); got != "chat" {
				return fmt.Errorf("openhands chat span operation %q", got)
			}
		case strings.HasPrefix(span.Name(), "execute_tool"):
			others++
			if got := stringAttr(span, "gen_ai.operation.name"); got != "execute_tool" {
				return fmt.Errorf("openhands tool span operation %q", got)
			}
		default:
			return fmt.Errorf("unexpected span %q in openhands canonical output", span.Name())
		}
	}
	if roots == 0 {
		return fmt.Errorf("no invoke_agent openhands root found")
	}
	if others == 0 {
		return fmt.Errorf("no chat or execute_tool children found under openhands root")
	}
	return nil
}
```

- [ ] **Step 4: Register the agent for live runs**

`validateCanonicalFile` (validator.go:16 area) gains:

```go
	case "openhands":
		return validateOpenHandsCanonicalTraces(traces, runID)
```

`live_test.go`: add `"openhands"` to the supported-agents switch at line 29;
add `"openhands"` to the RAW_TRACE_FILE-required condition at line 34; after
the opencode raw check add:

```go
		if lastErr == nil && agent == "openhands" {
			lastErr = validateOpenHandsRawFile(rawPath, runID)
		}
```

- [ ] **Step 5: Run the validator tests**

Run: `go test ./e2e/validator/ -v`
Expected: PASS — new tests plus the existing suite.

- [ ] **Step 6: Commit**

```bash
git add e2e/validator/validator.go e2e/validator/live_test.go e2e/validator/validator_test.go
git commit -m "test(e2e): validate openhands canonical fixture shape"
```

---

### Task 5: Opt-in paid live E2E stack

**Files:**
- Create: `compose.e2e-openhands.yaml`
- Create: `e2e/openhands/Dockerfile`
- Create: `e2e/openhands/run.sh`
- Create: `scripts/e2e-openhands.sh`
- Check: whether `scripts/check.sh` needs the new compose file registered for its compose checks (inspect how `compose.e2e-pi.yaml` is listed; follow it).

**Interfaces:**
- Consumes: shared collector service from `compose.e2e-base.yaml`; validator path `"openhands"` registered in Task 4.
- Produces: opt-in runner `scripts/e2e-openhands.sh`; captured raw + canonical fixtures land in `./.e2e-output` like every stack.

- [ ] **Step 1: Write the Compose stack**

`compose.e2e-openhands.yaml`:

```yaml
# OpenHands SDK e2e stack. The shared collector comes from
# compose.e2e-base.yaml; only the agent service is defined here, which keeps
# required credentials isolated per stack. The telemetry endpoint and
# protocol flow through env; nothing bakes into the image.
include:
  - compose.e2e-base.yaml

services:
  agent:
    build:
      context: e2e/openhands
      args:
        OPENHANDS_SDK_VERSION: "${OPENHANDS_SDK_VERSION:-}"
    environment:
      LLM_API_KEY: "${LLM_API_KEY:?set LLM_API_KEY or run scripts/e2e-openhands.sh}"
      LLM_MODEL: "${LLM_MODEL:-anthropic/claude-sonnet-4-5}"
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: "http://collector:4318/v1/traces"
      OTEL_EXPORTER_OTLP_TRACES_PROTOCOL: "http/protobuf"
      E2E_AGENT_TIMEOUT: "${E2E_AGENT_TIMEOUT:-10m}"
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-openhands.sh}"
    depends_on:
      collector:
        condition: service_healthy
```

(The collector listens on OTLP/HTTP 4318 per `collector-config.yaml`.)

- [ ] **Step 2: Write the image and entrypoint**

`e2e/openhands/Dockerfile`:

```dockerfile
FROM python:3.12-slim

ARG OPENHANDS_SDK_VERSION
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir "openhands-sdk${OPENHANDS_SDK_VERSION:+==${OPENHANDS_SDK_VERSION}}" \
    "openhands-tools${OPENHANDS_TOOLS_VERSION:+==${OPENHANDS_TOOLS_VERSION}}"

COPY run.sh /usr/local/bin/run-openhands-e2e
RUN chmod 0555 /usr/local/bin/run-openhands-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-openhands-e2e"]
```

Verify against PyPI whether `openhands-tools` installs separately at the
pinned version; if the SDK extra already brings it, drop that line rather
than guessing duplicates.

`e2e/openhands/run.sh`:

```sh
#!/bin/sh
set -eu

if [ -z "${LLM_API_KEY:-}" ]; then
  echo "LLM_API_KEY is required" >&2
  exit 1
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  python - <<'PY'
import os

from pydantic import SecretStr

from openhands.sdk import Agent, Conversation, LLM, Tool
from openhands.tools.terminal import TerminalTool

llm = LLM(
    usage_id="agent",
    model=os.environ["LLM_MODEL"],
    api_key=SecretStr(os.environ["LLM_API_KEY"]),
)
agent = Agent(llm=llm, tools=[Tool(TerminalTool)])
conversation = Conversation(agent=agent, workspace="/work")
conversation.send_message(
    "Use the bash tool exactly once to run 'printf openhands-otel-e2e'. "
    "Then reply with only: done."
)
conversation.run()
print("openhands e2e conversation finished")
PY
```

Match the pinned SDK version's actual exports: if `Tool` lives in
`openhands.sdk.tool` instead of the top level, adjust the import; build the
image once locally and iterate until the script runs green manually before
committing.

- [ ] **Step 3: Write the runner**

`scripts/e2e-openhands.sh` (mirror `scripts/e2e-pi.sh`; adapt to whatever
per-agent wiring `lib-e2e.sh` actually requires):

```bash
#!/usr/bin/env bash
# shellcheck disable=SC2034
set -euo pipefail

if [[ -z "${LLM_API_KEY:-}" ]]; then
  echo "LLM_API_KEY is required; this test runs a real paid model." >&2
  exit 2
fi

export E2E_AGENT=openhands

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

# shellcheck disable=SC2034
compose_files=(-f compose.e2e-openhands.yaml)
support_services=(collector)
e2e_run openhands
```

Read `lib-e2e.sh`'s `e2e_run` first: confirm how TRACE_FILE/RAW_TRACE_FILE
reach the validator and copy the pi entry's exact mechanics, including any
stack-name arguments.

- [ ] **Step 4: Verify the unpaid surface stays green**

Run: `docker compose -f compose.e2e-openhands.yaml config >/dev/null` and
then `./scripts/check.sh` from the repo root.
Expected: config validates; every check stage passes. The live run itself
stays opt-in and costs money; do not run it unprompted.

- [ ] **Step 5: Commit**

```bash
git add compose.e2e-openhands.yaml e2e/openhands/ scripts/e2e-openhands.sh scripts/check.sh
git commit -m "test(e2e): add opt-in openhands live stack"
```

---

### Task 6: Documentation and full check

**Files:**
- Modify: `README.md`
- Modify: `connector/codingagentconnector/README.md`
- Modify: `docs/design.md`
- Modify: `docs/harnesses.md`

**Interfaces:**
- Consumes: everything landed in Tasks 1–5.
- Produces: documentation matching shipped behavior; nothing downstream.

- [ ] **Step 1: Root README**

Add after the last supported-source bullet:

```markdown
- **OpenHands:** normalizes native OpenTelemetry traces from the OpenHands
  SDK (Laminar instrumentation, scope `lmnr.tracer`) into one canonical
  `invoke_agent openhands` trace per conversation, keyed on the SDK's
  session id. Delegate subagents arrive as sibling traces sharing the
  conversation id. Streamed completions carry no token usage upstream.
  Enable export with `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` plus
  `OTEL_EXPORTER=otlp_http`.
```

Add the OpenHands SDK observability docs link
(`https://docs.openhands.dev/sdk/guides/observability`) to References.

- [ ] **Step 2: Connector README**

Mirror the bullet in the connector-level source list, same links.

- [ ] **Step 3: `docs/design.md`**

1. The implemented-sources list in Known limitations and future work gains
   "OpenHands SDK native-trace normalization".
2. Add a short "OpenHands correlation model" section near the other edges:
   claiming markers, stateless sibling semantics for delegates, the
   streamed-usage gap, allowlist stripping over the content-heavy Laminar
   wire, research date 2026-08-23.

- [ ] **Step 4: `docs/harnesses.md`**

Move OpenHands from the "does not sort today" group to handled: the traces
edge claims scope `lmnr.tracer` groups carrying OpenHands markers; record
the no-cost/no-reasoning/streamed-usage findings and the desktop-UI
env-forwarding gap.

- [ ] **Step 5: Full CI surface**

Run: `./scripts/check.sh`
Expected: every stage passes. Fix anything this surfaces before pushing.

- [ ] **Step 6: Commit**

```bash
git add README.md connector/codingagentconnector/README.md docs/design.md docs/harnesses.md
git commit -m "docs: document openhands trace support"
```

---

## Self-Review Notes

- Spec coverage: claiming markers (Task 1); canonical mapping, reparenting,
  tool-call dedupe, synthetic roots, delegate linkage, tags/user identity
  (Task 1); router registration (Task 2); source-schema fixtures, replay,
  shuffle stability, content-presence guard (Task 3); validator helpers,
  live dispatcher entries, stripping-is-not-vacuous test (Task 4); opt-in
  paid stack (Task 5); docs migration (Task 6).
- Facts needing verification during implementation are flagged inside their
  tasks: the pinned SDK's Python exports for `run.sh` (Task 5), `lib-e2e.sh`
  mechanics and `check.sh` registration (Task 5), and the exact substring
  asserted from `rejectSensitiveAttrs` (Task 4). The streamed-no-usage /
  no-cost / no-reasoning wire facts come from source inspection (SDK
  `9421149`, lmnr 0.7.56, inspected 2026-08-23); refresh fixtures against
  source when either moves.
