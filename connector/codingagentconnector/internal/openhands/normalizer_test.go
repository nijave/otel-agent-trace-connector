// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
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
	span.SetName(spec.name)
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
	case []string:
		dst := attrs.PutEmptySlice(key)
		for _, s := range value {
			dst.AppendEmpty().SetStr(s)
		}
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
	return string(data)
}

func conversationSpec() spanSpec {
	return spanSpec{name: "conversation", spanID: "aaaaaaaaaaaaaaaa", attrs: baseAttrs()}
}

func TestClaimsMarkerGroupsOnly(t *testing.T) {
	s := &sink{}
	require.NoError(t, New(s).ConsumeTraces(context.Background(),
		makeTraces(makeSpan(traceA, conversationSpec()))))
	require.Len(t, s.batches, 1)

	unmarked := makeTraces(
		makeSpan(traceA, spanSpec{name: "my_function", spanID: "bbbbbbbbbbbbbbbb"}),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "b100000000000001", spanType: "LLM",
		}),
	)
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

func TestUnmarkedLLMTypeSpanDoesNotClaim(t *testing.T) {
	// An lmnr.tracer group whose only kept-role span is LLM-typed carries no
	// OpenHands marker (no marker name, no delegate flag); the edge must not
	// claim generic Laminar-instrumented traffic.
	s := &sink{}
	require.NoError(t, New(s).ConsumeTraces(context.Background(),
		makeTraces(makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "c000000000000001", spanType: "LLM",
		}))))
	require.Empty(t, s.batches)
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
	conv := conversationSpec()
	conv.attrs = attrs
	traces := makeTraces(
		makeSpan(traceA, conv),
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
				attrSessionID:               sessionID,
				"gen_ai.request.model":      "m",
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
	require.True(t, boolValue(root, delegateFlag))
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
