package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestValidateFileParsesActualOTLPJSON(t *testing.T) {
	traces := validTraces("run-1")
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	path := t.TempDir() + "/traces.json"
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	require.NoError(t, validateFile(path, "run-1"))
	require.Error(t, validateFile(path, "different-run"))
}

func TestValidateTracesRejectsMissingToolAndSensitiveData(t *testing.T) {
	traces := validTraces("run-1")
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	spans.RemoveIf(func(span ptrace.Span) bool { return stringAttr(span, "gen_ai.operation.name") == "execute_tool" })
	require.ErrorContains(t, validateTraces(traces, "run-1"), "execute_tool")

	traces = validTraces("run-1")
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("prompt", "must not leak")
	require.ErrorContains(t, validateTraces(traces, "run-1"), "sensitive root")
}

func validTraces(runID string) ptrace.Traces {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", runID)
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	traceID := pcommon.TraceID{1}
	rootID := pcommon.SpanID{2}
	root := spans.AppendEmpty()
	root.SetName("invoke_agent codex")
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.conversation.id", "conversation-1")
	root.Attributes().PutBool("coding_agent.turn.complete", true)
	chat := spans.AppendEmpty()
	chat.SetName("chat test")
	chat.SetTraceID(traceID)
	chat.SetSpanID(pcommon.SpanID{3})
	chat.SetParentSpanID(rootID)
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 1)
	tool := spans.AppendEmpty()
	tool.SetName("execute_tool shell")
	tool.SetTraceID(traceID)
	tool.SetSpanID(pcommon.SpanID{4})
	tool.SetParentSpanID(rootID)
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	return traces
}
