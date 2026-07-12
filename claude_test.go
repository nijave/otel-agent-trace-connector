package codingagentconnector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

func TestClaudeTraceNormalizerPreservesTreeAndAddsCanonicalSemantics(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "claude-code")
	rs.Resource().Attributes().PutStr("service.version", "2.3.4")
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	traceID := pcommon.TraceID{1}
	rootID := pcommon.SpanID{2}
	root := spans.AppendEmpty()
	root.SetName("claude_code.interaction")
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	root.Attributes().PutStr("session.id", "session-1")
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
	normalizer := &claudeTraceNormalizer{next: sink}
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

func TestFactoryCreatesClaudeTraceNormalizer(t *testing.T) {
	factory := NewFactory()
	instance, err := factory.CreateTracesToTraces(context.Background(), connectorSettings(), factory.CreateDefaultConfig(), &traceSink{})
	require.NoError(t, err)
	require.NotNil(t, instance)
}

func connectorSettings() connector.Settings {
	return connector.Settings{ID: component.NewID(componentType), TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
}
