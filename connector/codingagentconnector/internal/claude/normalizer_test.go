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

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
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
	normalizer := New(sink, true)
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
	normalizer := New(sink, true)
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
	// The prompt is redacted at the source and dropped from canonical output
	// entirely: raw leftovers never survive the strip.
	_, exists := root.Attributes().Get("user_prompt")
	require.False(t, exists, "raw user_prompt must not reach canonical output")
	for _, raw := range []string{"session.id", "user.id", "terminal.type", "span.type", "interaction.sequence", "interaction.duration_ms"} {
		_, exists := root.Attributes().Get(raw)
		require.False(t, exists, "raw %s must not reach canonical output", raw)
	}

	chat := findSpan(t, all, "chat glm-4.7")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, "chat", attrString(t, chat, "gen_ai.operation.name"))
	require.Equal(t, "glm-4.7", attrString(t, chat, "gen_ai.request.model"))
	// Totals span both llm_request spans in the capture.
	usage := map[string]int64{
		"gen_ai.usage.input_tokens":             477,
		"gen_ai.usage.output_tokens":            166,
		"gen_ai.usage.cache_read.input_tokens":  1216,
		"gen_ai.usage.cache_write.input_tokens": 0,
	}
	for key, want := range usage {
		total := int64(0)
		for i := 0; i < all.Len(); i++ {
			if value, ok := all.At(i).Attributes().Get(key); ok {
				total += value.Int()
			}
		}
		require.Equal(t, want, total, "%s must be remapped onto every chat span", key)
	}
	// Batches can arrive in any order, so latency/reason assertions collect
	// across every chat span instead of pinning one.
	var ttfts []float64
	finishReasons := map[string]bool{}
	for i := 0; i < all.Len(); i++ {
		span := all.At(i)
		if value, ok := span.Attributes().Get("gen_ai.response.time_to_first_chunk"); ok {
			ttfts = append(ttfts, value.Double())
		}
		if value, ok := span.Attributes().Get("gen_ai.response.finish_reasons"); ok {
			for j := 0; j < value.Slice().Len(); j++ {
				finishReasons[value.Slice().At(j).Str()] = true
			}
		}
	}
	require.ElementsMatch(t, []float64{2.188, 3.084, 1.197}, ttfts)
	require.True(t, finishReasons["end_turn"] && finishReasons["tool_use"], "stop reasons must be remapped: %v", finishReasons)
	for _, raw := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"ttft_ms", "stop_reason", "model", "duration_ms", "speed", "llm_request.context", "attempt",
		"success", "gen_ai.system"} {
		_, exists := chat.Attributes().Get(raw)
		require.False(t, exists, "raw %s must not reach canonical output", raw)
	}

	tool := findSpan(t, all, "execute_tool Bash")
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, "execute_tool", attrString(t, tool, "gen_ai.operation.name"))
	require.Equal(t, "Bash", attrString(t, tool, "gen_ai.tool.name"))
	assertRequiredKeys(t, tool)

	execution := findSpan(t, all, "claude_code.tool.execution")
	require.Equal(t, "execute_tool", attrString(t, execution, "gen_ai.operation.name"))
	assertRequiredKeys(t, execution)
	blocked := findSpan(t, all, "claude_code.tool.blocked_on_user")
	assertRequiredKeys(t, blocked)
}

// TestRemapUsageConvertsTTFTToSeconds pins the canonical TTFT unit: the wire
// carries integer milliseconds and canonical output stores fractional seconds.
func TestRemapUsageConvertsTTFTToSeconds(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutInt("ttft_ms", 250)
	remapUsage(attrs)
	value, ok := attrs.Get(ttftCanonicalKey)
	require.True(t, ok)
	require.Equal(t, pcommon.ValueTypeDouble, value.Type())
	require.InDelta(t, 0.25, value.Double(), 1e-9)
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	require.Equal(t, 2, all.Len(), "sub-tool spans must survive a batch of their own")
	// Sub-spans are not renamed but still carry the required canonical keys.
	outExecution := findSpan(t, all, "claude_code.tool.execution")
	require.Equal(t, pcommon.SpanID{4}, outExecution.ParentSpanID())
	assertRequiredKeys(t, outExecution)
	blockedOut := findSpan(t, all, "claude_code.tool.blocked_on_user")
	assertRequiredKeys(t, blockedOut)
}

// TestClaudeTraceNormalizerDropsNonNativeSiblingsInClaimedGroups covers a
// claimed group that also carries a sibling instrumentation scope: only
// claude_code.* spans are native, so sibling spans are dropped from canonical
// output while the input keeps them untouched.
func TestClaudeTraceNormalizerDropsNonNativeSiblingsInClaimedGroups(t *testing.T) {
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

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	findSpan(t, all, "invoke_agent claude_code")
	for i := 0; i < all.Len(); i++ {
		require.NotEqual(t, "plugin.chat", all.At(i).Name(),
			"non-native sibling spans must not reach canonical output")
	}
	// The input keeps the sibling span and its content: raw fidelity is the
	// raw pipeline's job.
	inSibling := input.ResourceSpans().At(0).ScopeSpans().At(1).Spans().At(0)
	require.Equal(t, "plugin.chat", inSibling.Name())
	require.Equal(t, "SENSITIVE", attrString(t, inSibling, "gen_ai.input.messages"))
}

func TestClaudeTraceNormalizerFiltersOtherProviders(t *testing.T) {
	input := ptrace.NewTraces()
	input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("codex.internal")
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
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

func assertRequiredKeys(t *testing.T, span ptrace.Span) {
	t.Helper()
	for _, key := range canonical.RequiredKeys() {
		_, ok := span.Attributes().Get(key)
		require.True(t, ok, "%s must carry required %s", span.Name(), key)
	}
}

// TestClaudeCapturesIdentity pins the identity mapping onto the interaction
// root: coding_agent.user.id is gated behind captureIdentity, but
// coding_agent.terminal.type is not (terminal type is not personal data).
func TestClaudeCapturesIdentity(t *testing.T) {
	buildInput := func() ptrace.Traces {
		input := ptrace.NewTraces()
		rs := input.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "claude-code")
		root := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		root.SetName("claude_code.interaction")
		root.Attributes().PutStr("user.id", "u-1")
		root.Attributes().PutStr("terminal.type", "iterm")
		return input
	}

	onSink := &traceSink{}
	require.NoError(t, New(onSink, true).ConsumeTraces(context.Background(), buildInput()))
	onRoot := findSpan(t, reassemble(onSink), "invoke_agent claude_code")
	require.Equal(t, "u-1", attrString(t, onRoot, "coding_agent.user.id"))
	require.Equal(t, "iterm", attrString(t, onRoot, "coding_agent.terminal.type"))

	offSink := &traceSink{}
	require.NoError(t, New(offSink, false).ConsumeTraces(context.Background(), buildInput()))
	offRoot := findSpan(t, reassemble(offSink), "invoke_agent claude_code")
	_, hasUser := offRoot.Attributes().Get("coding_agent.user.id")
	require.False(t, hasUser, "coding_agent.user.id must be gated behind captureIdentity")
	require.Equal(t, "iterm", attrString(t, offRoot, "coding_agent.terminal.type"),
		"terminal type is not gated behind captureIdentity")
}

func TestConsumeTracesFiltersResourceAttributes(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "claude-code")
	rs.Resource().Attributes().PutStr("service.version", "2.3.4")
	rs.Resource().Attributes().PutStr("session.id", "session-1")
	rs.Resource().Attributes().PutStr("vendor.thing", "x")
	root := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	root.SetName("claude_code.interaction")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	attrs := sink.all()[0].ResourceSpans().At(0).Resource().Attributes()
	for _, key := range []string{"service.name", "service.version"} {
		value, ok := attrs.Get(key)
		require.True(t, ok, "canonical resource key %s must survive", key)
		require.NotEmpty(t, value.Str())
	}
	for _, key := range []string{"session.id", "vendor.thing"} {
		_, ok := attrs.Get(key)
		require.False(t, ok, "raw key %s must not reach canonical output", key)
	}
	// session.id is consumed for conversation ids before the filter strips it.
	normalizedRoot := findSpan(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "invoke_agent claude_code")
	require.Equal(t, "session-1", attrString(t, normalizedRoot, "gen_ai.conversation.id"))
}
