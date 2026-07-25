package codex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
			testEvent("codex.sse_event", base.Add(4*time.Second), map[string]any{"event.kind": "response.completed", "model": "gpt-test", "input_token_count": "12", "output_token_count": int64(3), "cached_token_count": 2}),
		},
	}

	traces := buildTrace(turn, "completed", defaultScopeVersion)
	require.Equal(t, 3, traces.SpanCount())
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	root := findSpan(t, spans, "invoke_agent codex")
	chat := findSpan(t, spans, "chat gpt-test")
	tool := findSpan(t, spans, "execute_tool shell")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, root.TraceID(), chat.TraceID())
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.Equal(t, int64(12), attrInt(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(3), attrInt(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, "shell", attrString(t, tool, "gen_ai.tool.name"))
	require.Equal(t, 1, tool.Events().Len())
	require.Equal(t, "codex.tool_decision", tool.Events().At(0).Name())
	_, hasPrompt := root.Attributes().Get("prompt")
	_, hasArguments := tool.Attributes().Get("arguments")
	_, hasOutput := tool.Attributes().Get("output")
	require.False(t, hasPrompt)
	require.False(t, hasArguments)
	require.False(t, hasOutput)
	require.Equal(t, "codex_cli_rs", traces.ResourceSpans().At(0).Resource().Attributes().AsRaw()["service.name"])
}

func TestBuildTraceIDsAreDeterministic(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{conversationID: "c", first: base, last: base, promptSeen: true,
		events: []agentEvent{testEvent("codex.user_prompt", base, nil)}}
	first := buildTrace(turn, "shutdown", defaultScopeVersion)
	second := buildTrace(turn, "shutdown", defaultScopeVersion)
	require.Equal(t, first.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID(), second.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID())
}

func TestBuildTraceMarksTimeout(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{conversationID: "c", first: base, last: base,
		events: []agentEvent{testEvent("codex.api_request", base, nil)}}
	root := buildTrace(turn, "timeout", defaultScopeVersion).ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, ptrace.StatusCodeError, root.Status().Code())
	require.False(t, attrBool(t, root, "coding_agent.turn.complete"))
}

func TestBuildTraceAggregatesUsageAcrossModelCalls(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(2 * time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.sse_event", base.Add(time.Second), map[string]any{"event.kind": "response.completed", "input_token_count": 5, "output_token_count": 2}),
			testEvent("codex.sse_event", base.Add(2*time.Second), map[string]any{"event.kind": "response.completed", "input_token_count": 7, "output_token_count": 3}),
		},
	}
	root := buildTrace(turn, "completed", defaultScopeVersion).ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, int64(12), attrInt(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(5), attrInt(t, root, "gen_ai.usage.output_tokens"))
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
	spans := buildTrace(turn, "completed", defaultScopeVersion).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
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
	traces := buildTrace(turn, "completed", defaultScopeVersion)
	require.Equal(t, 2, traces.SpanCount()) // root + a single chat span
	chat := findSpan(t, traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "chat gpt-test")
	require.Equal(t, int64(10), attrInt(t, chat, "gen_ai.usage.input_tokens"))
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
	traces := buildTrace(turn, "completed", defaultScopeVersion)
	require.Equal(t, 2, traces.SpanCount()) // root + a single chat span
	chat := findSpan(t, traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "chat glm-test")
	_, ok := chat.Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, ok, "no usage was reported, so none should be invented")
}

// TestBuildTraceRecordsConfiguredModelProvider covers the provider label Codex
// reports on codex.conversation_starts. It stays in a coding_agent.* attribute
// rather than gen_ai.provider.name because the value is the operator-authored
// provider name from config.toml, not a known provider identifier.
func TestBuildTraceRecordsConfiguredModelProvider(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(time.Second), promptSeen: true,
		events: []agentEvent{
			testEvent("codex.conversation_starts", base, map[string]any{"provider_name": "z.ai via responses-proxy"}),
			testEvent("codex.user_prompt", base.Add(time.Millisecond), nil),
		},
	}
	root := buildTrace(turn, "completed", defaultScopeVersion).ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "z.ai via responses-proxy", attrString(t, root, "coding_agent.model_provider"))
	// The wire protocol Codex speaks is still OpenAI's, and that is what this means.
	require.Equal(t, "openai", attrString(t, root, "gen_ai.provider.name"))
}

// TestBuildTraceOmitsModelProviderWithoutConversationStart pins the known limit of
// the above: Codex emits codex.conversation_starts once per session, so a later turn
// in the same session has no provider label and must omit the attribute rather than
// invent or inherit one.
func TestBuildTraceOmitsModelProviderWithoutConversationStart(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{
		conversationID: "c", first: base, last: base.Add(time.Second), promptSeen: true,
		events: []agentEvent{testEvent("codex.user_prompt", base, nil)},
	}
	root := buildTrace(turn, "completed", defaultScopeVersion).ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	_, ok := root.Attributes().Get("coding_agent.model_provider")
	require.False(t, ok, "a turn with no conversation_starts must not claim a provider")
}

func testEvent(name string, timestamp time.Time, additional map[string]any) agentEvent {
	attrs := map[string]any{"event.name": name, "conversation.id": "conversation-1"}
	for key, value := range additional {
		attrs[key] = value
	}
	return agentEvent{name: name, conversationID: "conversation-1", timestamp: timestamp, attrs: attrs}
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
func attrBool(t *testing.T, span ptrace.Span, key string) bool {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	return value.Bool()
}
