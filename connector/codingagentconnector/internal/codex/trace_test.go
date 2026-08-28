package codex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestBuildTraceProducesCanonicalTree(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	turn := &turnState{
		conversationID: "conversation-1",
		first:          base, last: base.Add(4 * time.Second), promptSeen: true, completeSeen: true,
		resource: map[string]any{"service.name": "codex_cli_rs"},
		events: []agentEvent{
			testEvent("codex.user_prompt", base, map[string]any{"model": "gpt-test", "app.version": "1.2.3", "prompt": "secret"}),
			testEvent("codex.api_request", base.Add(2*time.Second), map[string]any{"duration_ms": "1000"}),
			testEvent("codex.tool_result", base.Add(3*time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1", "duration_ms": "200", "success": "true", "arguments": "secret command", "output": "secret output"}),
			// Equal timestamps and later batch order exercise order-independent
			// decision/result correlation without placing an event after span end.
			testEvent("codex.tool_decision", base.Add(3*time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1", "decision": "approved", "source": "Config"}),
			testEvent("codex.sse_event", base.Add(4*time.Second), map[string]any{"event.kind": "response.completed", "model": "gpt-test", "input_token_count": "12", "output_token_count": int64(3), "cached_token_count": 2, "tool_token_count": int64(15), "reasoning_token_count": 7, "ttft_ms": int64(40)}),
		},
	}

	traces := mustBuildTrace(t, turn, "completed")
	require.Equal(t, 3, traces.SpanCount())
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	root := findSpan(t, spans, "invoke_agent codex")
	chat := findSpan(t, spans, "chat gpt-test")
	tool := findSpan(t, spans, "execute_tool shell")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, root.TraceID(), chat.TraceID())
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.Equal(t, int64(12), attrInt(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(3), attrInt(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(2), attrInt(t, chat, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(15), attrInt(t, chat, "gen_ai.usage.total_tokens"))
	require.Equal(t, int64(7), attrInt(t, chat, "gen_ai.usage.reasoning.output_tokens"))
	require.Equal(t, 0.04, attrDouble(t, chat, "gen_ai.response.time_to_first_chunk"))
	require.Equal(t, "shell", attrString(t, tool, "gen_ai.tool.name"))
	require.Equal(t, 0, tool.Events().Len())
	_, hasPrompt := root.Attributes().Get("prompt")
	_, hasArguments := tool.Attributes().Get("arguments")
	_, hasOutput := tool.Attributes().Get("output")
	_, hasRootUsage := root.Attributes().Get("gen_ai.usage.input_tokens")
	_, hasToolCallID := tool.Attributes().Get("coding_agent.tool.call_id")
	_, hasToolSuccess := tool.Attributes().Get("coding_agent.tool.success")
	require.False(t, hasPrompt)
	require.False(t, hasArguments)
	require.False(t, hasOutput)
	require.False(t, hasRootUsage, "usage lives on chat spans only")
	require.False(t, hasToolCallID)
	require.False(t, hasToolSuccess)
	require.Equal(t, "codex_cli_rs", traces.ResourceSpans().At(0).Resource().Attributes().AsRaw()["service.name"])
}

func TestBuildTraceIDsAreDeterministic(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{conversationID: "c", first: base, last: base, promptSeen: true,
		events: []agentEvent{testEvent("codex.user_prompt", base, nil)}}
	first := mustBuildTrace(t, turn, "shutdown")
	second := mustBuildTrace(t, turn, "shutdown")
	require.Equal(t, first.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID(), second.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID())
}

// TestBuildTraceIDsDistinguishSameTimestampPrompts pins that two turns in one
// conversation whose user prompts share an identical timestamp get distinct
// trace IDs (the prompt fingerprint discriminates them), while re-emitting
// either turn reproduces its original ID.
func TestBuildTraceIDsDistinguishSameTimestampPrompts(t *testing.T) {
	base := time.Unix(100, 0)
	newTurn := func(prompt string) *turnState {
		return &turnState{conversationID: "c", first: base, last: base, promptSeen: true,
			events: []agentEvent{testEvent("codex.user_prompt", base, map[string]any{"prompt": prompt})}}
	}
	firstTurn, secondTurn := newTurn("first"), newTurn("second")
	firstID := mustBuildTrace(t, firstTurn, "shutdown").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID()
	secondID := mustBuildTrace(t, secondTurn, "shutdown").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID()
	require.NotEqual(t, firstID, secondID,
		"identical prompt timestamps must not collapse two turns into one trace ID")
	require.Equal(t, firstID, mustBuildTrace(t, firstTurn, "shutdown").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID())
	require.Equal(t, secondID, mustBuildTrace(t, secondTurn, "shutdown").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID())
}

func TestBuildTraceMarksTimeout(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{conversationID: "c", first: base, last: base,
		events: []agentEvent{testEvent("codex.api_request", base, nil)}}
	root := mustBuildTrace(t, turn, "timeout").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, ptrace.StatusCodeError, root.Status().Code())
	_, hasComplete := root.Attributes().Get("coding_agent.turn.complete")
	require.False(t, hasComplete)
}

// TestUsageStaysOnChatSpansNotRoot pins that each model call's token counts land
// on its own chat span and the invoke_agent root carries no usage of its own.
func TestUsageStaysOnChatSpansNotRoot(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(2 * time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{"event.kind": "response.completed", "input_token_count": 5, "output_token_count": 2}),
			testEvent("codex.sse_event", base.Add(2*time.Second), map[string]any{"event.kind": "response.completed", "input_token_count": 7, "output_token_count": 3}),
		},
	}
	spans := mustBuildTrace(t, turn, "completed").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	first := findSpan(t, spans, "chat")
	second := spans.At(2)
	require.Equal(t, int64(5), attrInt(t, first, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(7), attrInt(t, second, "gen_ai.usage.input_tokens"))
	for i := range spans.Len() {
		_, ok := spans.At(i).Attributes().Get("gen_ai.usage.input_tokens")
		if spans.At(i).Name() != "chat" {
			require.False(t, ok, "span %q must not carry usage", spans.At(i).Name())
		}
	}
}

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
	require.Equal(t, int64(42), attrInt(t, chat, "gen_ai.usage.cache_write.input_tokens"))
}

func TestChatRetryIsNotReusedByLaterCompletion(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(4 * time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.api_request", base.Add(time.Second), map[string]any{"duration_ms": 100}),
			testEvent("codex.api_request", base.Add(2*time.Second), map[string]any{"duration_ms": 100}),
			testEvent("codex.sse_event", base.Add(3*time.Second), map[string]any{"event.kind": "response.completed", "ttft_ms": 10}),
			testEvent("codex.sse_event", base.Add(4*time.Second), map[string]any{"event.kind": "response.completed", "ttft_ms": 10}),
		},
	}
	spans := mustBuildTrace(t, turn, "completed").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	require.True(t, base.Add(1900*time.Millisecond).Equal(spans.At(1).StartTimestamp().AsTime()))
	require.True(t, base.Equal(spans.At(2).StartTimestamp().AsTime()))
}

// TestBuildTraceSkipsTimingOnlyCompletion covers Codex emitting response.completed
// twice per model call: a timing-only record (duration_ms, no token counts) and
// the usage-bearing one. Only the usage-bearing completion should become a chat
// span, so the timing-only duplicate does not create a usage-less span.
func TestBuildTraceSkipsTimingOnlyCompletion(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{"event.kind": "response.completed", "model": "gpt-test", "duration_ms": 38}),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{"event.kind": "response.completed", "model": "gpt-test", "ttft_ms": 30, "input_token_count": 10, "output_token_count": 4}),
		},
	}
	traces := mustBuildTrace(t, turn, "completed")
	require.Equal(t, 2, traces.SpanCount()) // root + a single chat span
	chat := findSpan(t, traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "chat gpt-test")
	require.Equal(t, int64(10), attrInt(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, 0.03, attrDouble(t, chat, "gen_ai.response.time_to_first_chunk"))
}

// TestBuildTraceKeepsCompletionWithoutUsage covers a provider that omits token usage
// from the stream (a chat-completions endpoint without stream_options.include_usage,
// for example). Codex still logs both response.completed records, so the duplicate
// must still be skipped -- but the surviving call has to keep its chat span rather
// than disappearing from the trace just because no token counts arrived.
func TestBuildTraceKeepsCompletionWithoutUsage(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{"event.kind": "response.completed", "model": "glm-test", "duration_ms": 38}),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{"event.kind": "response.completed", "model": "glm-test", "ttft_ms": 30}),
		},
	}
	traces := mustBuildTrace(t, turn, "completed")
	require.Equal(t, 2, traces.SpanCount()) // root + a single chat span
	chat := findSpan(t, traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "chat glm-test")
	_, ok := chat.Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, ok, "no usage was reported, so none should be invented")
}

func TestBuildTraceReportsResourceCopyFailure(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{conversationID: "c", first: base, last: base,
		// chan int cannot round-trip into pdata; FromRaw rejects it.
		resource: map[string]any{"service.name": "codex_cli_rs", "poison": make(chan int)},
		events:   []agentEvent{testEvent("codex.api_request", base, nil)}}
	traces, err := buildTrace(turn, "shutdown", DefaultScopeVersion)
	require.Error(t, err)
	require.NotNil(t, traces)
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	require.Equal(t, 1, spans.Len())
	require.Equal(t, "invoke_agent codex", spans.At(0).Name())
}

func testEvent(name string, timestamp time.Time, additional map[string]any) agentEvent {
	attrs := map[string]any{"event.name": name, "conversation.id": "conversation-1"}
	for key, value := range additional {
		attrs[key] = value
	}
	return agentEvent{name: name, conversationID: "conversation-1", timestamp: timestamp, attrs: attrs}
}

func TestBuildTraceHugeDurationKeepsSpanBoundsSane(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{conversationID: "c", first: base, last: base.Add(time.Second),
		events: []agentEvent{
			testEvent("codex.tool_result", base.Add(time.Second), map[string]any{"tool_name": "shell", "duration_ms": "10000000000000000"}),
		}}
	spans := mustBuildTrace(t, turn, "completed").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	tool := findSpan(t, spans, "execute_tool shell")
	require.False(t, tool.StartTimestamp().AsTime().After(tool.EndTimestamp().AsTime()),
		"an overflowing duration_ms must not move the start past the end")
}

func mustBuildTrace(t *testing.T, turn *turnState, reason string) ptrace.Traces {
	t.Helper()
	traces, err := buildTrace(turn, reason, DefaultScopeVersion)
	require.NoError(t, err)
	return traces
}

func findSpan(t *testing.T, spans ptrace.SpanSlice, name string) ptrace.Span {
	t.Helper()
	for i := 0; i < spans.Len(); i++ {
		if spans.At(i).Name() == name {
			return spans.At(i)
		}
	}
	require.FailNow(t, "span not found", name)
	return ptrace.Span{}
}

func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	return value.Str()
}
func attrInt(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	return value.Int()
}

func TestBuildTraceFiltersVendorResourceAttributes(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	turn := &turnState{
		conversationID: "conversation-1",
		first:          base, last: base, promptSeen: true,
		resource: map[string]any{"service.name": "codex_cli_rs", "service.version": "0.1.0", "vendor.thing": "x"},
	}

	traces := mustBuildTrace(t, turn, "completed")
	attrs := traces.ResourceSpans().At(0).Resource().Attributes()
	for _, key := range []string{"service.name", "service.version"} {
		value, ok := attrs.Get(key)
		require.True(t, ok, "canonical resource key %s must survive", key)
		require.NotEmpty(t, value.Str())
	}
	_, hasVendor := attrs.Get("vendor.thing")
	require.False(t, hasVendor, "vendor resource keys must not reach canonical output")
}

func attrDouble(t *testing.T, span ptrace.Span, key string) float64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	require.Equal(t, pcommon.ValueTypeDouble, value.Type(), "%s must be a double", key)
	return value.Double()
}
