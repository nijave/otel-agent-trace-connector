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

	all := reassemble(sink)
	root := findSpan(t, all, "invoke_agent claude_code")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "interaction root must stay a root")
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.NotEmpty(t, attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "anthropic", attrString(t, root, "gen_ai.provider.name"))
	require.Equal(t, "native", attrString(t, root, "coding_agent.source"))
	require.Equal(t, "2.1.207", attrString(t, root, "coding_agent.client.version"))
	// The prompt is redacted at the source and must stay redacted after normalization.
	require.Equal(t, "<REDACTED>", attrString(t, root, "user_prompt"))

	chat := findSpan(t, all, "chat glm-4.7")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, "chat", attrString(t, chat, "gen_ai.operation.name"))
	require.Equal(t, "glm-4.7", attrString(t, chat, "gen_ai.request.model"))

	tool := findSpan(t, all, "execute_tool Bash")
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, "execute_tool", attrString(t, tool, "gen_ai.operation.name"))
	require.Equal(t, "Bash", attrString(t, tool, "gen_ai.tool.name"))
}

// TestClaudeTraceNormalizerKeepsSubToolOnlyBatch covers an export carrying only tool
// sub-spans. Claude Code exports a span when it ends, so a child can land in a batch
// holding none of its ancestors; recognizing just the three renamed span types
// dropped that whole batch and silently deleted those spans from the trace.
func TestClaudeTraceNormalizerKeepsSubToolOnlyBatch(t *testing.T) {
	input := ptrace.NewTraces()
	spans := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()
	execution := spans.AppendEmpty()
	execution.SetName("claude_code.tool.execution")
	execution.SetParentSpanID(pcommon.SpanID{4})
	spans.AppendEmpty().SetName("claude_code.tool.blocked_on_user")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	require.Equal(t, 2, all.Len(), "sub-tool spans must survive a batch of their own")
	// Sub-spans pass through unchanged; only the three canonical types are renamed.
	require.Equal(t, pcommon.SpanID{4}, findSpan(t, all, "claude_code.tool.execution").ParentSpanID())
}

// TestClaudeTraceNormalizerStripsContentFromUnmatchedScopesInClaimedGroups
// covers a claimed group that also carries a sibling instrumentation scope:
// only claude_code.* spans are native, so sibling spans keep their shape but
// lose prompt/completion/tool content before canonical export.
func TestClaudeTraceNormalizerStripsContentFromUnmatchedScopesInClaimedGroups(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	nativeScope := rs.ScopeSpans().AppendEmpty()
	nativeScope.Spans().AppendEmpty().SetName("claude_code.interaction")

	siblingScope := rs.ScopeSpans().AppendEmpty()
	siblingScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	sibling := siblingScope.Spans().AppendEmpty()
	sibling.SetName("plugin.chat")
	sibling.Attributes().PutStr("gen_ai.input.messages", "SENSITIVE")
	sibling.Events().AppendEmpty().SetName("gen_ai.choice")
	sibling.Events().AppendEmpty().SetName("plugin.step")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	findSpan(t, all, "invoke_agent claude_code")
	outSibling := findSpan(t, all, "plugin.chat")
	_, exists := outSibling.Attributes().Get("gen_ai.input.messages")
	require.False(t, exists, "content must not ride along on non-native scopes in a claimed group")
	require.Equal(t, 1, outSibling.Events().Len(), "content events are removed, non-content events survive")
	// The input keeps its content: raw fidelity is the raw pipeline's job.
	inSibling := input.ResourceSpans().At(0).ScopeSpans().At(1).Spans().At(0)
	require.Equal(t, "SENSITIVE", attrString(t, inSibling, "gen_ai.input.messages"))
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

// reassemble flattens every span the sink received into one slice, the way a trace
// backend stitches a trace back together from separate exports.
func reassemble(sink *traceSink) ptrace.SpanSlice {
	all := ptrace.NewSpanSlice()
	for _, traces := range sink.all() {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					spans.At(k).CopyTo(all.AppendEmpty())
				}
			}
		}
	}
	return all
}

func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	return value.Str()
}
