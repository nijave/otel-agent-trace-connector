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

func TestValidateClaudeRawAndCanonicalTraces(t *testing.T) {
	raw := validClaudeTraces("run-claude", false)
	canonical := validClaudeTraces("run-claude", true)
	require.NoError(t, validateClaudeRawTraces(raw, "run-claude"))
	require.NoError(t, validateCanonicalTraces(canonical, "run-claude", "claude_code"))

	raw.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(2).Attributes().PutStr("full_command", "sensitive")
	require.ErrorContains(t, validateClaudeRawTraces(raw, "run-claude"), "sensitive Claude")
}

func TestValidateClaudeTracesRejectsMissingRawToolAndCanonicalModel(t *testing.T) {
	raw := validClaudeTraces("run-claude", false)
	rawSpans := raw.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	rawSpans.RemoveIf(func(span ptrace.Span) bool { return span.Name() == "claude_code.tool" })
	require.ErrorContains(t, validateClaudeRawTraces(raw, "run-claude"), "LLM or tool")

	canonical := validClaudeTraces("run-claude", true)
	canonical.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(1).Attributes().Remove("gen_ai.request.model")
	require.ErrorContains(t, validateCanonicalTraces(canonical, "run-claude", "claude_code"), "model")
}

func TestValidateClaudeTracesRejectsContentEventOnGrandchild(t *testing.T) {
	traces := validClaudeTraces("run-claude", false)
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	root := spans.At(0)
	grandchild := spans.AppendEmpty()
	grandchild.SetName("claude_code.tool.execution")
	grandchild.SetTraceID(root.TraceID())
	grandchild.SetSpanID(pcommon.SpanID{14})
	grandchild.SetParentSpanID(spans.At(2).SpanID())
	grandchild.Events().AppendEmpty().SetName("tool.output")
	require.ErrorContains(t, validateClaudeRawTraces(traces, "run-claude"), "span event")
}

func validateFile(path, runID string) error {
	return validateCanonicalFile(path, runID, "codex")
}

func validateTraces(traces ptrace.Traces, runID string) error {
	return validateCanonicalTraces(traces, runID, "codex")
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

func validClaudeTraces(runID string, canonical bool) ptrace.Traces {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", runID)
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	traceID := pcommon.TraceID{10}
	rootID := pcommon.SpanID{11}
	root := spans.AppendEmpty()
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	root.Attributes().PutStr("user_prompt", "<REDACTED>")
	llm := spans.AppendEmpty()
	llm.SetTraceID(traceID)
	llm.SetSpanID(pcommon.SpanID{12})
	llm.SetParentSpanID(rootID)
	tool := spans.AppendEmpty()
	tool.SetTraceID(traceID)
	tool.SetSpanID(pcommon.SpanID{13})
	tool.SetParentSpanID(rootID)
	if canonical {
		root.SetName("invoke_agent claude_code")
		root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		root.Attributes().PutStr("gen_ai.conversation.id", "session-claude")
		root.Attributes().PutStr("gen_ai.provider.name", "anthropic")
		root.Attributes().PutStr("coding_agent.client.name", "claude_code")
		root.Attributes().PutStr("telemetry.source", "native")
		llm.SetName("chat claude-haiku")
		llm.Attributes().PutStr("gen_ai.operation.name", "chat")
		llm.Attributes().PutStr("gen_ai.request.model", "claude-haiku")
		tool.SetName("execute_tool Bash")
		tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
		tool.Attributes().PutStr("gen_ai.tool.name", "Bash")
	} else {
		root.SetName("claude_code.interaction")
		llm.SetName("claude_code.llm_request")
		llm.Attributes().PutStr("model", "claude-haiku")
		tool.SetName("claude_code.tool")
		tool.Attributes().PutStr("tool_name", "Bash")
	}
	return traces
}
