package validator

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
	require.ErrorContains(t, validateTraces(traces, "run-1"), "sensitive attribute")
}

// TestValidateTracesRejectsSensitiveAttrOnGrandchild guards the whole span set, not
// just the root and its direct children: a leak deeper in the tree must still fail.
func TestValidateTracesRejectsSensitiveAttrOnGrandchild(t *testing.T) {
	traces := validTraces("run-1")
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	grandchild := spans.AppendEmpty()
	grandchild.SetName("execute_tool inner")
	grandchild.SetTraceID(spans.At(0).TraceID())
	grandchild.SetSpanID(pcommon.SpanID{9})
	grandchild.SetParentSpanID(spans.At(2).SpanID())
	grandchild.Attributes().PutStr("arguments", "must not leak")
	require.ErrorContains(t, validateTraces(traces, "run-1"), "sensitive attribute")
}

// TestValidateTracesAcceptsRunWithAnIncompleteTurn covers a run that contains more
// than one root: the Codex connector emits a root per turn, and a turn finalized by
// inactivity timeout is incomplete by design. Validation must keep looking rather
// than failing on whichever candidate comes first.
func TestValidateTracesAcceptsRunWithAnIncompleteTurn(t *testing.T) {
	// The timed-out turn is emitted first, so it is the first candidate the root
	// scan encounters -- exactly the ordering that made this fail before.
	incompleteOnly := ptrace.NewTraces()
	rs := incompleteOnly.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	incomplete := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	incomplete.SetName("invoke_agent codex")
	incomplete.SetTraceID(pcommon.TraceID{7})
	incomplete.SetSpanID(pcommon.SpanID{8})
	incomplete.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	incomplete.Attributes().PutStr("gen_ai.conversation.id", "conversation-1")
	incomplete.Attributes().PutBool("coding_agent.turn.complete", false)

	// A run holding only the incomplete turn still fails, and says why.
	require.ErrorContains(t, validateTraces(incompleteOnly, "run-1"), "incomplete")

	// Append the complete turn after it: the incomplete root must not veto the run.
	traces := ptrace.NewTraces()
	incompleteOnly.ResourceSpans().CopyTo(traces.ResourceSpans())
	validTraces("run-1").ResourceSpans().MoveAndAppendTo(traces.ResourceSpans())
	spans := collectRunSpans(traces, "run-1")
	require.False(t, boolAttr(spans[0], "coding_agent.turn.complete"), "incomplete root must be first")
	require.NoError(t, validateTraces(traces, "run-1"))
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

func TestValidateClaudeRawFileParsesActualOTLPJSON(t *testing.T) {
	traces := validClaudeTraces("run-claude", false)
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	path := t.TempDir() + "/raw.json"
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	require.NoError(t, validateClaudeRawFile(path, "run-claude"))
	require.Error(t, validateClaudeRawFile(path, "different-run"))
}

func TestValidateOpenAIAdhocCanonical(t *testing.T) {
	traces := ptrace.NewTraces()
	for _, service := range []string{"openai-adhoc-legacy", "openai-adhoc-latest"} {
		rs := traces.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
		span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		span.SetName("chat glm-4.7")
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("gen_ai.provider.name", "openai")
		span.Attributes().PutStr("telemetry.source", "native")
		span.Attributes().PutStr("coding_agent.client.name", service)
		span.Attributes().PutInt("gen_ai.usage.input_tokens", 5)
		span.Attributes().PutInt("gen_ai.usage.output_tokens", 6)
	}
	require.NoError(t, validateCanonicalTraces(traces, "run-1", "openai_adhoc"))

	// A surviving legacy gen_ai.system must fail validation.
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).
		Attributes().PutStr("gen_ai.system", "openai")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "openai_adhoc"))
}

func TestValidateStrandsCanonicalRequiresTreeWithoutContent(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	traceID := pcommon.TraceID{1}
	root := spans.AppendEmpty()
	root.SetName("invoke_agent strands-e2e")
	root.SetTraceID(traceID)
	root.SetSpanID(pcommon.SpanID{2})
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.provider.name", "strands-agents")
	root.Attributes().PutStr("telemetry.source", "native")

	chat := spans.AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.SetTraceID(traceID)
	chat.SetSpanID(pcommon.SpanID{3})
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 5)

	tool := spans.AppendEmpty()
	tool.SetName("execute_tool get_marker")
	tool.SetTraceID(traceID)
	tool.SetSpanID(pcommon.SpanID{4})
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.tool.name", "get_marker")

	require.NoError(t, validateCanonicalTraces(traces, "run-1", "strands"))

	// Content events must fail canonical validation.
	chat.Events().AppendEmpty().SetName("gen_ai.user.message")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "strands"))
}

func TestValidateStrandsCanonicalSkipsFailedChatAttempts(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	traceID := pcommon.TraceID{1}
	root := spans.AppendEmpty()
	root.SetName("invoke_agent strands-e2e")
	root.SetTraceID(traceID)
	root.SetSpanID(pcommon.SpanID{2})
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.provider.name", "strands-agents")
	root.Attributes().PutStr("telemetry.source", "native")

	tool := spans.AppendEmpty()
	tool.SetName("execute_tool get_marker")
	tool.SetTraceID(traceID)
	tool.SetSpanID(pcommon.SpanID{4})
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.tool.name", "get_marker")

	// A retried rate limit leaves a chat span without usage; alone it must fail.
	failed := spans.AppendEmpty()
	failed.SetName("chat glm-4.7")
	failed.SetTraceID(traceID)
	failed.SetSpanID(pcommon.SpanID{5})
	failed.Attributes().PutStr("gen_ai.operation.name", "chat")
	failed.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "strands"),
		"a tree whose only chat attempt failed has no usage evidence")

	// The successful retry after it satisfies the tree.
	success := spans.AppendEmpty()
	success.SetName("chat glm-4.7")
	success.SetTraceID(traceID)
	success.SetSpanID(pcommon.SpanID{6})
	success.Attributes().PutStr("gen_ai.operation.name", "chat")
	success.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	success.Attributes().PutInt("gen_ai.usage.input_tokens", 5)
	require.NoError(t, validateCanonicalTraces(traces, "run-1", "strands"))
}

func TestValidateStrandsRawRequiresContentEvidence(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("chat")
	require.Error(t, validateStrandsRawTraces(traces, "run-1"),
		"raw output without content events makes the stripping assertion vacuous")
	span.Events().AppendEmpty().SetName("gen_ai.user.message")
	require.NoError(t, validateStrandsRawTraces(traces, "run-1"))
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
