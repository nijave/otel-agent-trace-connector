// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	require.NoError(t, New(s, true).ConsumeTraces(context.Background(), traces))
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
	require.NoError(t, New(s, true).ConsumeTraces(context.Background(),
		makeTraces(makeSpan(traceA, conversationSpec()))))
	require.Len(t, s.batches, 1)

	unmarked := makeTraces(
		makeSpan(traceA, spanSpec{name: "my_function", spanID: "bbbbbbbbbbbbbbbb"}),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "b100000000000001", spanType: "LLM",
		}),
	)
	s2 := &sink{}
	require.NoError(t, New(s2, true).ConsumeTraces(context.Background(), unmarked))
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
	require.NoError(t, New(s3, true).ConsumeTraces(context.Background(), foreign))
	require.Empty(t, s3.batches)
}

func TestUnmarkedLLMTypeSpanDoesNotClaim(t *testing.T) {
	// An lmnr.tracer group whose only kept-role span is LLM-typed carries no
	// OpenHands marker (no marker name, no delegate flag); the edge must not
	// claim generic Laminar-instrumented traffic.
	s := &sink{}
	require.NoError(t, New(s, true).ConsumeTraces(context.Background(),
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
	attrs["lmnr.association.properties.user_id"] = "42"
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
	require.Equal(t, "native", attrString(r, "coding_agent.source"))
	require.Equal(t, "openhands", attrString(r, "coding_agent.client.name"))
	require.Equal(t, scopeName, attrString(r, "coding_agent.source.scope"))
	// User identity and tags are outside the canonical vocabulary; the raw
	// keys must not survive in any renamed form.
	_, ok := r.Attributes().Get("enduser.pseudo.id")
	require.False(t, ok)
	_, ok = r.Attributes().Get("coding_agent.openhands.tag.team")
	require.False(t, ok)
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
				"gen_ai.system":                            "openai",
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
	require.Equal(t, "openai", attrString(chat, "gen_ai.provider.name"))
	_, hasSystem := chat.Attributes().Get("gen_ai.system")
	require.False(t, hasSystem, "raw gen_ai.system must not survive")
	require.Equal(t, "anthropic/claude-sonnet-4-5", attrString(chat, "gen_ai.request.model"))
	require.Equal(t, int64(100), intValue(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(200), intValue(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(360), intValue(t, chat, "gen_ai.usage.total_tokens"))
	require.Equal(t, int64(50), intValue(t, chat, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(10), intValue(t, chat, "gen_ai.usage.cache_write.input_tokens"))
	_, hasRawTotal := chat.Attributes().Get("llm.usage.total_tokens")
	require.False(t, hasRawTotal, "raw total_tokens must be remapped")
	_, ok := chat.Attributes().Get("gen_ai.input.messages")
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
	sum := sha256.New()
	_, _ = sum.Write(traceA[:])
	_, _ = sum.Write([]byte(syntheticRootDiscriminator))
	var bounds [16]byte
	binary.BigEndian.PutUint64(bounds[:8], uint64(pcommon.NewTimestampFromTime(baseTime)))
	binary.BigEndian.PutUint64(bounds[8:], uint64(pcommon.NewTimestampFromTime(baseTime.Add(time.Second))))
	_, _ = sum.Write(bounds[:])
	var want pcommon.SpanID
	copy(want[:], sum.Sum(nil)[:8])
	require.Equal(t, want, roots[0].SpanID())
	// The synthetic root inherits the conversation id from a child span.
	require.Equal(t, sessionID, attrString(roots[0], "gen_ai.conversation.id"))
}

func TestFragmentWindowsGetDistinctStableRoots(t *testing.T) {
	// Successive partial exports of one conversation share a trace ID but
	// cover different time windows. Each fragment must get its own root
	// span ID — identical IDs would collide in backends keying by
	// (traceID, spanID) — while re-emitting the same fragment reproduces
	// the same ID.
	fragment := func(start time.Time) ptrace.Traces {
		return makeTraces(
			makeSpan(traceA, spanSpec{
				name: "agent.step", spanID: "dddddddddddddddd",
				start: start,
				end:   start.Add(time.Second),
				attrs: baseAttrs(),
			}),
			makeSpan(traceA, spanSpec{
				name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", spanType: "LLM",
				start: start,
				end:   start.Add(time.Second),
			}),
		)
	}
	first := normalizeOne(t, fragment(baseTime))
	second := normalizeOne(t, fragment(baseTime.Add(time.Minute)))
	root1 := findByName(t, first, "invoke_agent openhands")[0]
	root2 := findByName(t, second, "invoke_agent openhands")[0]
	require.Equal(t, traceA, root1.TraceID())
	require.Equal(t, traceA, root2.TraceID())
	require.NotEqual(t, root1.SpanID(), root2.SpanID(),
		"distinct fragments of one conversation must not share a root span ID")

	rerunFirst := normalizeOne(t, fragment(baseTime))
	rerunSecond := normalizeOne(t, fragment(baseTime.Add(time.Minute)))
	require.Equal(t, root1.SpanID(), findByName(t, rerunFirst, "invoke_agent openhands")[0].SpanID(),
		"re-emitting a fragment must reproduce its root span ID")
	require.Equal(t, root2.SpanID(), findByName(t, rerunSecond, "invoke_agent openhands")[0].SpanID(),
		"re-emitting a fragment must reproduce its root span ID")
}

func TestOutputByteIdenticalAcrossRuns(t *testing.T) {
	// Attribute insertion order must not depend on Go map iteration: two
	// runs over one input batch marshal to byte-identical JSON.
	traces := makeTraces(
		makeSpan(traceA, spanSpec{
			name: "agent.step", spanID: "dddddddddddddddd",
			attrs: map[string]any{
				attrSessionID:                   sessionID,
				"lmnr.association.properties.a": "1",
				"lmnr.association.properties.b": "2",
				"lmnr.association.properties.c": "3",
				"lmnr.association.properties.d": "4",
				"lmnr.association.properties.e": "5",
			},
		}),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", spanType: "LLM",
			attrs: map[string]any{
				"gen_ai.system":             "openai",
				"gen_ai.request.model":      "m",
				"gen_ai.usage.input_tokens": int64(7),
			},
		}),
	)
	want := marshalT(t, normalizeOne(t, traces))
	for i := 0; i < 10; i++ {
		require.Equal(t, want, marshalT(t, normalizeOne(t, traces)),
			"run %d diverged from the first run's bytes", i+2)
	}
}

func TestOutputScopeCarriesWireVersion(t *testing.T) {
	traces := makeTraces(makeSpan(traceA, spanSpec{
		name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", spanType: "LLM",
		attrs: map[string]any{attrSessionID: sessionID, attrIsDelegate: "true"},
	}))
	out := normalizeOne(t, traces)
	require.Equal(t, 1, out.ResourceSpans().Len())
	scopes := out.ResourceSpans().At(0).ScopeSpans()
	require.Equal(t, 1, scopes.Len())
	require.Equal(t, scopeName, scopes.At(0).Scope().Name())
	require.Equal(t, "0.7.56", scopes.At(0).Scope().Version())
}

func TestDelegateLinkageDropped(t *testing.T) {
	// The delegate flag still claims the group, but linkage detail is
	// outside the canonical vocabulary and must not reach output.
	attrs := baseAttrs()
	attrs[attrIsDelegate] = "true"
	attrs[attrMetadata+"task_id"] = "task-9"
	attrs[attrMetadata+"subagent_type"] = "bash_delegate"
	attrs[attrMetadata+"parent_session_id"] = "parent-conversation-uuid"
	delegated := makeTraces(makeSpan(traceB, spanSpec{
		name: "conversation", spanID: "cccccccccccccccc", attrs: attrs,
	}))
	out := normalizeOne(t, delegated)
	root := findByName(t, out, "invoke_agent openhands")[0]
	root.Attributes().Range(func(k string, _ pcommon.Value) bool {
		require.NotContains(t, k, "coding_agent.openhands.delegate")
		return true
	})
}

func TestTagsAssociationPropertyDropped(t *testing.T) {
	attrs := baseAttrs()
	attrs["lmnr.association.properties.tags"] = []string{"delegate"}
	delegated := makeTraces(makeSpan(traceB, spanSpec{
		name: "conversation", spanID: "cccccccccccccccc", attrs: attrs,
	}))
	out := normalizeOne(t, delegated)
	root := findByName(t, out, "invoke_agent openhands")[0]
	_, ok := root.Attributes().Get("coding_agent.openhands.tags")
	require.False(t, ok)
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
	conv := conversationSpec()
	conv.attrs = map[string]any{
		attrSessionID:                         sessionID,
		"lmnr.association.properties.user_id": "42",
		"conversation.tags.team":              "core",
	}
	out := normalizeOne(t, makeTraces(
		makeSpan(traceA, conv),
		makeSpan(traceA, spanSpec{
			name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", spanType: "LLM",
			attrs: map[string]any{
				"lmnr.span.path":        `["conversation"]`,
				"lmnr.span.sdk_version": "0.7.56",
			},
		}),
	))
	droppedPrefixes := []string{
		"lmnr.span.",
		"enduser.pseudo.id",
		"coding_agent.openhands.",
	}
	for _, span := range allSpansOf(out) {
		span.Attributes().Range(func(k string, _ pcommon.Value) bool {
			for _, prefix := range droppedPrefixes {
				if strings.HasPrefix(k, prefix) {
					t.Errorf("bookkeeping attribute %q leaked onto %s", k, span.Name())
				}
			}
			return true
		})
	}
}

// TestConversationCapturesIdentity pins coding_agent.user.id on the
// invoke_agent root, sourced from the conversation span's Laminar user-id
// association property and gated behind captureIdentity.
func TestConversationCapturesIdentity(t *testing.T) {
	attrs := baseAttrs()
	attrs[attrUserID] = "42"
	conv := conversationSpec()
	conv.attrs = attrs

	on := normalizeOne(t, makeTraces(makeSpan(traceA, conv)))
	onRoot := findByName(t, on, "invoke_agent openhands")[0]
	require.Equal(t, "42", attrString(onRoot, "coding_agent.user.id"))

	s := &sink{}
	require.NoError(t, New(s, false).ConsumeTraces(context.Background(), makeTraces(makeSpan(traceA, conv))))
	require.Len(t, s.batches, 1)
	offRoot := findByName(t, s.batches[0], "invoke_agent openhands")[0]
	_, hasUser := offRoot.Attributes().Get("coding_agent.user.id")
	require.False(t, hasUser, "coding_agent.user.id must be gated behind captureIdentity")
}

func TestConsumeTracesFiltersResourceAttributes(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "agent-server")
	rs.Resource().Attributes().PutStr("service.version", "0.9.0")
	rs.Resource().Attributes().PutStr("vendor.thing", "x")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(scopeName)
	makeSpan(traceA, conversationSpec()).CopyTo(ss.Spans().AppendEmpty())

	s := &sink{}
	require.NoError(t, New(s, true).ConsumeTraces(context.Background(), input))
	require.Len(t, s.batches, 1)
	attrs := s.batches[0].ResourceSpans().At(0).Resource().Attributes()
	for _, key := range []string{"service.name", "service.version"} {
		value, ok := attrs.Get(key)
		require.True(t, ok, "canonical resource key %s must survive", key)
		require.NotEmpty(t, value.Str())
	}
	_, hasVendor := attrs.Get("vendor.thing")
	require.False(t, hasVendor, "vendor resource keys must not reach canonical output")
}
