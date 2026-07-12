package codingagentconnector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestBuildTraceProducesCanonicalTree(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	turn := &turnState{
		key:   turnKey{provider: "codex", conversationID: "conversation-1"},
		first: base, last: base.Add(4 * time.Second), promptSeen: true, completeSeen: true,
		resource: map[string]any{"service.name": "codex_cli_rs"},
		events: []agentEvent{
			testEvent("codex.user_prompt", base, map[string]any{"model": "gpt-test", "app.version": "1.2.3", "prompt": "secret"}),
			testEvent("codex.api_request", base.Add(2*time.Second), map[string]any{"duration_ms": "1000"}),
			testEvent("codex.tool_decision", base.Add(2500*time.Millisecond), map[string]any{"tool_name": "shell", "call_id": "call-1", "decision": "approved", "source": "Config"}),
			testEvent("codex.tool_result", base.Add(3*time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1", "duration_ms": "200", "success": "true", "arguments": "secret command", "output": "secret output"}),
			testEvent("codex.sse_event", base.Add(4*time.Second), map[string]any{"event.kind": "response.completed", "model": "gpt-test", "input_token_count": "12", "output_token_count": int64(3), "cached_token_count": 2}),
		},
	}

	traces := buildTrace(turn, "completed")
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
	turn := &turnState{key: turnKey{provider: "codex", conversationID: "c"}, first: base, last: base, promptSeen: true,
		events: []agentEvent{testEvent("codex.user_prompt", base, nil)}}
	first := buildTrace(turn, "shutdown")
	second := buildTrace(turn, "shutdown")
	require.Equal(t, first.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID(), second.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID())
}

func TestBuildTraceMarksTimeout(t *testing.T) {
	base := time.Unix(100, 0)
	turn := &turnState{key: turnKey{provider: "codex", conversationID: "c"}, first: base, last: base,
		events: []agentEvent{testEvent("codex.api_request", base, nil)}}
	root := buildTrace(turn, "timeout").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, ptrace.StatusCodeError, root.Status().Code())
	require.False(t, attrBool(t, root, "coding_agent.turn.complete"))
}

func testEvent(name string, timestamp time.Time, additional map[string]any) agentEvent {
	attrs := map[string]any{"event.name": name, "conversation.id": "conversation-1"}
	for key, value := range additional {
		attrs[key] = value
	}
	return agentEvent{name: name, provider: "codex", conversationID: "conversation-1", timestamp: timestamp, attrs: attrs}
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
