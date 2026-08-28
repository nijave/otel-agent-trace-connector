package validator

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// TestValidateFileRejectsFileWithNoValidBatches pins that a corrupt or
// truncated exporter file is reported as such: skipping unparseable lines must
// not surface as the misleading "run id was not found".
func TestValidateFileRejectsFileWithNoValidBatches(t *testing.T) {
	path := t.TempDir() + "/corrupt.json"
	require.NoError(t, os.WriteFile(path, []byte("{\"resourceSpans\": [\nnot-json\n"), 0o600))
	err := validateFile(path, "run-1")
	require.ErrorContains(t, err, "no valid OTLP batches")
	require.NotContains(t, err.Error(), "run id was not found")
}

// TestValidateFileSkipsCorruptLinesAmongValidOnes keeps the per-line
// tolerance: individual bad lines never fail an otherwise valid export.
func TestValidateFileSkipsCorruptLinesAmongValidOnes(t *testing.T) {
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(validTraces("run-1"))
	require.NoError(t, err)
	path := t.TempDir() + "/mixed.json"
	content := "garbage\n" + string(encoded) + "\n{\"trunc"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	require.NoError(t, validateFile(path, "run-1"))
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

// TestValidateTracesAcceptsRunWithAnIncompleteTurn covers a run that contains
// more than one root candidate: the Codex connector emits a root per turn, and
// a turn finalized by inactivity timeout is incomplete (here: still attached to
// its turn parent, with no conversation id of its own). That broken candidate
// must not veto the run when a later candidate validates.
func TestValidateTracesAcceptsRunWithAnIncompleteTurn(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	incomplete := spans.AppendEmpty()
	incomplete.SetName("invoke_agent codex")
	incomplete.SetTraceID(pcommon.TraceID{1})
	incomplete.SetSpanID(pcommon.SpanID{2})
	incomplete.SetParentSpanID(pcommon.SpanID{3})

	traceID := pcommon.TraceID{9}
	complete := spans.AppendEmpty()
	complete.SetName("invoke_agent codex")
	complete.SetTraceID(traceID)
	complete.SetSpanID(pcommon.SpanID{10})
	complete.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	complete.Attributes().PutStr("gen_ai.conversation.id", "conversation-1")

	chat := spans.AppendEmpty()
	chat.SetName("chat test")
	chat.SetTraceID(traceID)
	chat.SetParentSpanID(pcommon.SpanID{10})
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 1)

	tool := spans.AppendEmpty()
	tool.SetName("execute_tool shell")
	tool.SetTraceID(traceID)
	tool.SetParentSpanID(pcommon.SpanID{10})
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")

	require.NoError(t, validateTraces(traces, "run-1"))

	// With no valid candidate left, the diagnostic from the last one surfaces.
	complete.Attributes().Remove("gen_ai.conversation.id")
	require.ErrorContains(t, validateTraces(traces, "run-1"), "conversation")
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
		span.Attributes().PutStr("coding_agent.source", "native")
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

// TestValidatePiCanonicalRequiresTreeWithoutContent pins the canonical shape
// the Pi normalizer emits for one user input: multiple invoke_agent roots (one
// per agentic iteration), each chat child carrying model and usage, and the
// bash tool call attached under an agent root.
func TestValidatePiCanonicalRequiresTreeWithoutContent(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	traceID := pcommon.TraceID{1}
	root := spans.AppendEmpty()
	root.SetName("invoke_agent pi")
	root.SetTraceID(traceID)
	root.SetSpanID(pcommon.SpanID{2})
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.agent.name", "pi")
	root.Attributes().PutStr("gen_ai.conversation.id", "session-1")
	root.Attributes().PutStr("coding_agent.source", "native")

	chat := spans.AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.SetTraceID(traceID)
	chat.SetParentSpanID(pcommon.SpanID{2})
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 5)

	tool := spans.AppendEmpty()
	tool.SetName("execute_tool bash")
	tool.SetTraceID(traceID)
	tool.SetParentSpanID(pcommon.SpanID{2})
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.tool.name", "bash")

	require.NoError(t, validateCanonicalTraces(traces, "run-1", "pi"))

	// Content events must fail canonical validation.
	chat.Events().AppendEmpty().SetName("gen_ai.user.message")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "pi"))
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
	root.Attributes().PutStr("coding_agent.source", "native")

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
	root.Attributes().PutStr("coding_agent.source", "native")

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

// The live caller sits behind the e2e build tag, which the linter does not
// build with; this untagged exercise of the file wrapper keeps it used.
func TestValidateStrandsRawFileParsesActualOTLPJSON(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-strands")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("chat")
	span.Events().AppendEmpty().SetName("gen_ai.user.message")
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	path := t.TempDir() + "/raw.json"
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	require.NoError(t, validateStrandsRawFile(path, "run-strands"))
	require.Error(t, validateStrandsRawFile(path, "different-run"))
}

// TestValidateCopilotCanonicalRequiresTreeWithoutContent pins the canonical
// shape the GenAI edge emits for Copilot CLI: one valid invoke_agent root whose
// subject is producer-chosen (BYOK renames it), usage on both directions, and
// chat plus execute_tool children without content.
func TestValidateCopilotCanonicalRequiresTreeWithoutContent(t *testing.T) {
	traces := validCopilotTraces()
	require.NoError(t, validateCanonicalTraces(traces, "run-1", "copilot"))

	// A different producer-chosen subject must still validate.
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	spans.At(0).SetName("invoke_agent byok-agent")
	require.NoError(t, validateCanonicalTraces(traces, "run-1", "copilot"))

	// Content events must fail canonical validation.
	chat := spans.At(1)
	chat.Events().AppendEmpty().SetName("gen_ai.user.message")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "copilot"))
}

func TestValidateCopilotCanonicalRejectsIncompleteTree(t *testing.T) {
	traces := validCopilotTraces()
	rootAttrs := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	rootAttrs.Remove("gen_ai.usage.output_tokens")
	err := validateCanonicalTraces(traces, "run-1", "copilot")
	require.ErrorContains(t, err, "output usage")

	traces = validCopilotTraces()
	chat := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(1)
	chat.Attributes().Remove("gen_ai.request.model")
	err = validateCanonicalTraces(traces, "run-1", "copilot")
	require.ErrorContains(t, err, "request model")

	traces = validCopilotTraces()
	rootAttrs = traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	rootAttrs.Remove("coding_agent.client.name")
	err = validateCanonicalTraces(traces, "run-1", "copilot")
	require.ErrorContains(t, err, "client name")

	traces = validCopilotTraces()
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	spans.RemoveIf(func(span ptrace.Span) bool { return stringAttr(span, "gen_ai.operation.name") == "execute_tool" })
	err = validateCanonicalTraces(traces, "run-1", "copilot")
	require.ErrorContains(t, err, "execute_tool")
}

func validCopilotTraces() ptrace.Traces {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	traceID := pcommon.TraceID{1}
	root := spans.AppendEmpty()
	root.SetName("invoke_agent copilot-cli")
	root.SetTraceID(traceID)
	root.SetSpanID(pcommon.SpanID{2})
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.agent.name", "copilot-cli")
	root.Attributes().PutStr("gen_ai.conversation.id", "session-1")
	root.Attributes().PutInt("gen_ai.usage.input_tokens", 5)
	root.Attributes().PutInt("gen_ai.usage.output_tokens", 7)
	root.Attributes().PutStr("coding_agent.source", "native")
	root.Attributes().PutStr("coding_agent.client.name", "github-copilot")

	chat := spans.AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.SetTraceID(traceID)
	chat.SetParentSpanID(pcommon.SpanID{2})
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.request.model", "glm-4.7")

	tool := spans.AppendEmpty()
	tool.SetName("execute_tool shell")
	tool.SetTraceID(traceID)
	tool.SetParentSpanID(pcommon.SpanID{2})
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	return traces
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
		root.Attributes().PutStr("coding_agent.source", "native")
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

func TestCursorCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "cursor", "testdata", "cursor-canonical.otlp.json")
	require.NoError(t, validateCursorCanonicalFile(path))
}

func TestRejectOpenCodeContent(t *testing.T) {
	leaky := ptrace.NewTraces()
	span := leaky.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("invoke_agent opencode")
	span.Attributes().PutStr("ai.response.text", "leak")
	err := rejectOpenCodeContent(allSpans(leaky))
	require.ErrorContains(t, err, "ai.response.text")

	clean := ptrace.NewTraces()
	ok := clean.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	ok.SetName("invoke_agent opencode")
	ok.Attributes().PutInt("gen_ai.usage.input_tokens", 10)
	require.NoError(t, rejectOpenCodeContent(allSpans(clean)))
}

func TestValidateOpenCodeRawTraces(t *testing.T) {
	raw := ptrace.NewTraces()
	rs := raw.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	ss := rs.ScopeSpans().AppendEmpty()
	llm := ss.Spans().AppendEmpty()
	llm.SetName("ai.streamText")
	noise := ss.Spans().AppendEmpty()
	noise.SetName("sql.execute")
	tool := ss.Spans().AppendEmpty()
	tool.SetName("ai.toolCall")
	tool.Attributes().PutStr("ai.toolCall.name", "bash")

	require.NoError(t, validateOpenCodeRawTraces(raw, "run-1"))
	require.Error(t, validateOpenCodeRawTraces(ptrace.NewTraces(), "run-1"), "empty run must fail")
}

func TestValidateOpenCodeRawFileParsesActualOTLPJSON(t *testing.T) {
	raw := ptrace.NewTraces()
	rs := raw.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Spans().AppendEmpty().SetName("ai.streamText")
	tool := ss.Spans().AppendEmpty()
	tool.SetName("ai.toolCall")
	tool.Attributes().PutStr("ai.toolCall.name", "bash")

	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(raw)
	require.NoError(t, err)
	path := t.TempDir() + "/raw.json"
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	require.NoError(t, validateOpenCodeRawFile(path, "run-1"))
	require.Error(t, validateOpenCodeRawFile(path, "different-run"))
}

func TestOpenHandsCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "openhands", "testdata", "openhands-canonical.otlp.json")
	require.NoError(t, validateOpenHandsCanonicalFile(path))
}

// TestValidateOpenHandsCanonicalIgnoresOtherRuns pins run-id filtering: spans
// from another agent sharing the same live export file must not be validated
// as openhands output.
func TestValidateOpenHandsCanonicalIgnoresOtherRuns(t *testing.T) {
	traces := ptrace.NewTraces()

	target := traces.ResourceSpans().AppendEmpty()
	target.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	targetSpans := target.ScopeSpans().AppendEmpty().Spans()
	root := targetSpans.AppendEmpty()
	root.SetName("invoke_agent openhands")
	root.Attributes().PutStr("gen_ai.conversation.id", "session-1")
	root.Attributes().PutStr("gen_ai.agent.name", "openhands")
	chat := targetSpans.AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.SetParentSpanID(root.SpanID())
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")

	foreign := traces.ResourceSpans().AppendEmpty()
	foreign.Resource().Attributes().PutStr("e2e.run.id", "run-2")
	junk := foreign.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	junk.SetName("sql.execute")

	require.NoError(t, validateCanonicalTraces(traces, "run-1", "openhands"))
	require.ErrorContains(t, validateCanonicalTraces(traces, "run-2", "openhands"),
		"unexpected span")
}

// TestOpenHandsRawFixtureValidates exercises validateOpenHandsRawFile over the
// committed wire-reference fixture without the e2e build tag, keeping it used
// for the linter.
func TestOpenHandsRawFixtureValidates(t *testing.T) {
	fixture := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "openhands", "testdata", "openhands-native-traces.json")
	raw, err := os.ReadFile(fixture)
	require.NoError(t, err)
	// The fixture is pretty-printed for review; the file wrapper reads
	// collector-style JSONL, so compact before parsing.
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, raw))
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, compact.Bytes(), 0o600))
	require.NoError(t, validateOpenHandsRawFile(path, ""))
}

// TestOpenHandsCanonicalFixtureRejectsContent injects the content attribute the
// raw lmnr.tracer wire actually carries; canonical validation must catch it.
func TestOpenHandsCanonicalFixtureRejectsContent(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "openhands", "testdata", "openhands-canonical.otlp.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(raw)
	require.NoError(t, err)
	span := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	span.Attributes().PutStr("gen_ai.input.messages", `[{"role":"user","content":"leak"}]`)

	temp := filepath.Join(t.TempDir(), "leaky.json")
	data, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(temp, data, 0o600))

	err = validateOpenHandsCanonicalFile(temp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "survived normalization")
}

func TestStrandsCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "genai", "testdata", "strands-canonical.otlp.json")
	require.NoError(t, validateCanonicalFile(path, "strands-otel-1787880919-619255", "strands"))
}

func TestOpenAIAdhocCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "genai", "testdata", "openai-adhoc-canonical.otlp.json")
	require.NoError(t, validateCanonicalFile(path, "openai-otel-1787880965-636343", "openai_adhoc"))
}

func TestCopilotCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "genai", "testdata", "copilot-cli-canonical.otlp.json")
	require.NoError(t, validateCanonicalFile(path, "copilot-byok-1787498490", "copilot"))
}

func TestCodexCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "codex", "testdata", "codex-canonical.otlp.json")
	require.NoError(t, validateCanonicalFile(path, "codex-otel-1787881086-648014", "codex"))
}

func TestClaudeCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "claude", "testdata", "claude-canonical.otlp.json")
	require.NoError(t, validateCanonicalFile(path, "claude-otel-1787956985-1645736", "claude_code"))
}

// firstFixtureBatch parses the first JSONL line of a committed canonical
// fixture so a rejection test can inject forbidden content into it.
func firstFixtureBatch(t *testing.T, path string) ptrace.Traces {
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	line, _, _ := bytes.Cut(raw, []byte("\n"))
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(line)
	require.NoError(t, err)
	require.Positive(t, traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().Len())
	return traces
}

// writeInjected serializes an injected batch the same way the collector's file
// exporter does, so validateCanonicalFile reads it back through the live path.
func writeInjected(t *testing.T, traces ptrace.Traces) string {
	temp := filepath.Join(t.TempDir(), "leaky.json")
	data, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(temp, data, 0o600))
	return temp
}

// TestClaudeCanonicalFixtureRejectsContent injects the span event
// rejectClaudeTraceContent must catch on the committed fixture; canonical
// validation cannot pass content.
func TestClaudeCanonicalFixtureRejectsContent(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "claude", "testdata", "claude-canonical.otlp.json")
	traces := firstFixtureBatch(t, path)
	span := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	span.Events().AppendEmpty().SetName("api_request_body")

	err := validateCanonicalFile(writeInjected(t, traces), "claude-otel-1787956985-1645736", "claude_code")
	require.Error(t, err)
	require.ErrorContains(t, err, `sensitive Claude span event "api_request_body"`)
}

// TestCodexCanonicalFixtureRejectsContent injects an attribute from the shared
// sensitive-attribute check; the codex edge has no agent-specific content
// rejector beyond it.
func TestCodexCanonicalFixtureRejectsContent(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "codex", "testdata", "codex-canonical.otlp.json")
	traces := firstFixtureBatch(t, path)
	span := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	span.Attributes().PutStr("prompt", "leak")

	err := validateCanonicalFile(writeInjected(t, traces), "codex-otel-1787881086-648014", "codex")
	require.Error(t, err)
	require.ErrorContains(t, err, `sensitive attribute "prompt"`)
}
