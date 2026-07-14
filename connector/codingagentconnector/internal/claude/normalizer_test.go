package claude

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type traceSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := ptrace.NewTraces()
	traces.CopyTo(copied)
	s.traces = append(s.traces, copied)
	return nil
}
func (s *traceSink) all() []ptrace.Traces {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ptrace.Traces(nil), s.traces...)
}

func TestClaudeTraceNormalizerPreservesTreeAndAddsCanonicalSemantics(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "claude-code")
	rs.Resource().Attributes().PutStr("service.version", "2.3.4")
	rs.Resource().Attributes().PutStr("session.id", "session-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	traceID := pcommon.TraceID{1}
	rootID := pcommon.SpanID{2}
	root := spans.AppendEmpty()
	root.SetName("claude_code.interaction")
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	llm := spans.AppendEmpty()
	llm.SetName("claude_code.llm_request")
	llm.SetTraceID(traceID)
	llm.SetSpanID(pcommon.SpanID{3})
	llm.SetParentSpanID(rootID)
	llm.Attributes().PutStr("model", "claude-test")
	tool := spans.AppendEmpty()
	tool.SetName("claude_code.tool")
	tool.SetTraceID(traceID)
	tool.SetSpanID(pcommon.SpanID{4})
	tool.SetParentSpanID(rootID)
	tool.Attributes().PutStr("tool_name", "Bash")

	sink := &traceSink{}
	normalizer := New(sink)
	require.NoError(t, normalizer.ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	output := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	normalizedRoot := findSpan(t, output, "invoke_agent claude_code")
	normalizedLLM := findSpan(t, output, "chat claude-test")
	normalizedTool := findSpan(t, output, "execute_tool Bash")
	require.Equal(t, traceID, normalizedRoot.TraceID())
	require.Equal(t, rootID, normalizedLLM.ParentSpanID())
	require.Equal(t, rootID, normalizedTool.ParentSpanID())
	require.Equal(t, "session-1", attrString(t, normalizedRoot, "gen_ai.conversation.id"))
	require.Equal(t, "anthropic", attrString(t, normalizedLLM, "gen_ai.provider.name"))
	require.Equal(t, "claude_code", attrString(t, normalizedTool, "coding_agent.client.name"))
	require.Equal(t, "2.3.4", attrString(t, normalizedRoot, "coding_agent.client.version"))
	// The input is not mutated because Collector fan-out may send it elsewhere.
	require.Equal(t, "claude_code.interaction", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name())
}

// TestClaudeTraceNormalizerAgainstRealCapture pins the normalizer to a real
// Claude Code 2.1.207 run (GLM-4.7) captured by the e2e harness. Unlike the
// hand-built input above, this guards against upstream schema drift: if a future
// Claude Code release renames its native spans or attributes, this test fails and
// tells us to update the normalizer. The capture is split across several OTLP
// batches (one per span flush, interaction root last), so feeding each batch
// separately also exercises the stateless, order-independent path.
func TestClaudeTraceNormalizerAgainstRealCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-native-traces.json")
	require.NoError(t, err)
	unmarshaler := &ptrace.JSONUnmarshaler{}
	sink := &traceSink{}
	normalizer := New(sink)
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		batch, err := unmarshaler.UnmarshalTraces(line)
		require.NoError(t, err)
		require.NoError(t, normalizer.ConsumeTraces(context.Background(), batch))
	}

	// Reassemble every emitted span the way a trace backend would.
	var all []ptrace.Span
	for _, traces := range sink.all() {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					all = append(all, spans.At(k))
				}
			}
		}
	}

	root := findSpanIn(t, all, "invoke_agent claude_code")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "interaction root must stay a root")
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.NotEmpty(t, attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "anthropic", attrString(t, root, "gen_ai.provider.name"))
	require.Equal(t, "native", attrString(t, root, "telemetry.source"))
	require.Equal(t, "2.1.207", attrString(t, root, "coding_agent.client.version"))
	// The prompt is redacted at the source and must stay redacted after normalization.
	require.Equal(t, "<REDACTED>", attrString(t, root, "user_prompt"))

	chat := findSpanIn(t, all, "chat glm-4.7")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, "chat", attrString(t, chat, "gen_ai.operation.name"))
	require.Equal(t, "glm-4.7", attrString(t, chat, "gen_ai.request.model"))

	tool := findSpanIn(t, all, "execute_tool Bash")
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, "execute_tool", attrString(t, tool, "gen_ai.operation.name"))
	require.Equal(t, "Bash", attrString(t, tool, "gen_ai.tool.name"))
}

func TestClaudeTraceNormalizerFiltersOtherProviders(t *testing.T) {
	input := ptrace.NewTraces()
	input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("codex.internal")
	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all())
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

func findSpanIn(t *testing.T, spans []ptrace.Span, name string) ptrace.Span {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
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
