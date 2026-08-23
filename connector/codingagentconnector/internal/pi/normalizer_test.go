package pi

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

// piInput builds a resource group shaped like the observed wire data: the
// extension exports OTLP/HTTP traces whose scope is its package name and whose
// spans use a custom attribute vocabulary.
func piInput() (ptrace.Traces, pcommon.SpanID) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "pi")
	scope := rs.ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()
	turn := spans.AppendEmpty()
	turn.SetName("chat-turn")
	turn.SetSpanID(pcommon.SpanID{2})
	turn.SetParentSpanID(pcommon.SpanID{9})
	turn.Attributes().PutStr("sessionId", "session-1")
	turn.Attributes().PutStr("durationMs", "809")
	turn.Attributes().PutStr("eventType", "chat_turn_completed")
	turn.Attributes().PutStr("langfuse.observation.type", "span")
	turn.Attributes().PutStr("langfuse.observation.level", "DEFAULT")

	llm := spans.AppendEmpty()
	llm.SetName("llm-generation [main] [request]")
	llm.SetTraceID(pcommon.TraceID{1})
	llm.SetSpanID(pcommon.SpanID{3})
	llm.SetParentSpanID(pcommon.SpanID{2})
	llm.Attributes().PutStr("model", "deepseek-v4-flash")
	llm.Attributes().PutStr("provider", "opencode-go")
	llm.Attributes().PutStr("sessionId", "session-1")
	llm.Attributes().PutStr("usage.input", "3335")
	llm.Attributes().PutStr("usage.output", "3")
	llm.Attributes().PutStr("usage.total_tokens", "3338")
	llm.Attributes().PutStr("usage.cache_read", "0")
	llm.Attributes().PutStr("usage.cache_write", "0")
	llm.Attributes().PutStr("usage.cost.total", "0.00023387")
	llm.Attributes().PutStr("usage", `{"input":3335,"output":3,"cost":{"total":0.00023387}}`)
	llm.Attributes().PutStr("stopReason", "stop")
	llm.Attributes().PutStr("status", "completed")
	llm.Attributes().PutStr("langfuse.observation.type", "span")
	return input, pcommon.SpanID{9}
}

func TestPiTraceNormalizerRebuildsCanonicalTree(t *testing.T) {
	input, phantomParent := piInput()

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	root := findSpan(t, spans, "invoke_agent pi")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(),
		"a parent absent from the batch must not survive; invoke_agent must be a real root")
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.Equal(t, "pi", attrString(t, root, "gen_ai.agent.name"))
	require.Equal(t, "session-1", attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "pi", attrString(t, root, "coding_agent.client.name"))
	require.Equal(t, "native", attrString(t, root, "telemetry.source"))
	require.Equal(t, "chat-turn", attrString(t, root, "coding_agent.source.event"))

	chat := findSpan(t, spans, "chat deepseek-v4-flash")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, "chat", attrString(t, chat, "gen_ai.operation.name"))
	require.Equal(t, "deepseek-v4-flash", attrString(t, chat, "gen_ai.request.model"))
	require.Equal(t, "opencode-go", attrString(t, chat, "gen_ai.provider.name"))
	require.Equal(t, int64(3335), attrInt(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(3), attrInt(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(3338), attrInt(t, chat, "gen_ai.usage.total_tokens"))
	// The dotted semconv cache keys must be renamed even when the wire value
	// is zero, not dropped with the source key.
	require.Equal(t, int64(0), attrInt(t, chat, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(0), attrInt(t, chat, "gen_ai.usage.cache_creation.input_tokens"))

	for _, span := range []ptrace.Span{root, chat} {
		for _, key := range []string{
			"langfuse.observation.type",
			"langfuse.observation.level",
			"usage",
			"usage.input",
			"usage.output",
			"usage.total_tokens",
			"usage.cache_read",
			"usage.cache_write",
			"usage.cost.total",
		} {
			_, ok := span.Attributes().Get(key)
			require.False(t, ok, "%s must not reach canonical output", key)
		}
	}
	require.NotEqual(t, phantomParent, root.ParentSpanID())
	require.Equal(t, "chat-turn", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name(),
		"input must not be mutated; Collector fan-out may send it elsewhere")
}

func TestPiTraceNormalizerClaimsByScopeOrSDKResource(t *testing.T) {
	byResource := ptrace.NewTraces()
	rs := byResource.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "pi")
	rs.Resource().Attributes().PutStr("telemetry.sdk.name", "@amaster.ai/pi-telemetry")
	rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("chat-turn")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), byResource))
	require.Len(t, sink.all(), 1, "resource telemetry.sdk.name claims the group")

	other := ptrace.NewTraces()
	other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("codex.internal")
	sinkOther := &traceSink{}
	require.NoError(t, New(sinkOther).ConsumeTraces(context.Background(), other))
	require.Empty(t, sinkOther.all())
}

func TestPiTraceNormalizerMatchesGenerationNamePrefixes(t *testing.T) {
	for _, name := range []string{"llm-generation [main] [request]", "llm-generation [sidechain] [2]"} {
		input := ptrace.NewTraces()
		scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
		scope.Scope().SetName("@amaster.ai/pi-telemetry")
		spans := scope.Spans()
		turn := spans.AppendEmpty()
		turn.SetName("chat-turn")
		turn.SetSpanID(pcommon.SpanID{2})
		gen := spans.AppendEmpty()
		gen.SetName(name)
		gen.SetParentSpanID(pcommon.SpanID{2})
		gen.Attributes().PutStr("model", "glm-5.3")

		sink := &traceSink{}
		require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
		all := reassemble(sink)
		require.Equal(t, "chat glm-5.3", findSpan(t, all, "chat glm-5.3").Name())
	}
}

// TestPiTraceNormalizerMapsToolSpansByAttributeName pins how tools appear on
// the wire (live capture 2026-08-22): the span NAME is the bare tool name
// ("bash"), the identity lives in the toolName/toolCallId attributes, and the
// referenced parent can be absent from the batch.
func TestPiTraceNormalizerMapsToolSpansByAttributeName(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()
	turn := spans.AppendEmpty()
	turn.SetName("chat-turn")
	turn.SetSpanID(pcommon.SpanID{2})
	tool := spans.AppendEmpty()
	tool.SetName("bash")
	tool.SetParentSpanID(pcommon.SpanID{7}) // phantom: never exported
	tool.Attributes().PutStr("toolName", "bash")
	tool.Attributes().PutStr("toolCallId", "call_9c4debe87115427c83aa8826")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	root := findSpan(t, all, "invoke_agent pi")
	normalizedTool := findSpan(t, all, "execute_tool bash")
	require.Equal(t, root.SpanID(), normalizedTool.ParentSpanID(),
		"orphaned tool span must attach to the agent root in the same batch")
	require.Equal(t, "bash", attrString(t, normalizedTool, "gen_ai.tool.name"))
	require.Equal(t, "execute_tool", attrString(t, normalizedTool, "gen_ai.operation.name"))
	require.Equal(t, "call_9c4debe87115427c83aa8826", attrString(t, normalizedTool, "gen_ai.tool.call.id"))
}

// TestPiTraceNormalizerReparentsOrphansToFirstAgentRoot covers the observed
// multi-turn shape: one trace per user input holds several agentic iterations,
// every span references a phantom parent, and each iteration emits its own
// chat-turn span. Each chat-turn becomes an invoke_agent root; orphaned
// children attach to the first root in the batch rather than dangle.
func TestPiTraceNormalizerReparentsOrphansToFirstAgentRoot(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()
	phantom := pcommon.SpanID{9}

	gen1 := spans.AppendEmpty()
	gen1.SetName("llm-generation [main] [request]")
	gen1.SetParentSpanID(phantom)
	gen1.Attributes().PutStr("model", "deepseek-v4-flash")
	bash := spans.AppendEmpty()
	bash.SetName("bash")
	bash.SetParentSpanID(phantom)
	bash.Attributes().PutStr("toolName", "bash")
	gen2 := spans.AppendEmpty()
	gen2.SetName("llm-generation [main] [request]")
	gen2.SetParentSpanID(phantom)
	gen2.Attributes().PutStr("model", "deepseek-v4-flash")
	firstTurn := spans.AppendEmpty()
	firstTurn.SetName("chat-turn")
	firstTurn.SetSpanID(pcommon.SpanID{2})
	secondTurn := spans.AppendEmpty()
	secondTurn.SetName("chat-turn")
	secondTurn.SetSpanID(pcommon.SpanID{3})

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)

	var roots []ptrace.Span
	zero := pcommon.SpanID{}
	for i := 0; i < all.Len(); i++ {
		if span := all.At(i); span.ParentSpanID() == zero && span.Name() == "invoke_agent pi" {
			roots = append(roots, span)
		}
	}
	require.Len(t, roots, 2, "each chat-turn iteration becomes an invoke_agent root")
	for _, name := range []string{"chat deepseek-v4-flash", "execute_tool bash"} {
		require.Equal(t, roots[0].SpanID(), findSpan(t, all, name).ParentSpanID(),
			"%s must attach to the first agent root instead of dangling", name)
	}
}

// TestPiTraceNormalizerReparentsModellessChatChildren pins that a generation
// span without a model attribute — normalized to bare "chat" with no suffix —
// still counts as a canonical child and attaches to the agent root instead of
// dangling on its unexported parent.
func TestPiTraceNormalizerReparentsModellessChatChildren(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()
	turn := spans.AppendEmpty()
	turn.SetName("chat-turn")
	turn.SetSpanID(pcommon.SpanID{2})
	gen := spans.AppendEmpty()
	gen.SetName("llm-generation [main] [request]")
	gen.SetParentSpanID(pcommon.SpanID{9}) // absent from the batch
	// no model attribute, so the canonical name is bare "chat"

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	root := findSpan(t, all, "invoke_agent pi")
	require.Equal(t, root.SpanID(), findSpan(t, all, "chat").ParentSpanID(),
		"a chat child without a model suffix must attach to the agent root instead of dangling")
}

// TestPiTraceNormalizerStripsContentFromUnmatchedScopesInClaimedGroups
// covers a claimed group that also carries a sibling instrumentation scope:
// the scope/sdk.name claim sweeps every instrumentation in the process into
// this edge, so non-native spans keep their shape but lose
// prompt/completion/tool content before canonical export.
func TestPiTraceNormalizerStripsContentFromUnmatchedScopesInClaimedGroups(t *testing.T) {
	input, _ := piInput()
	rs := input.ResourceSpans().At(0)
	siblingScope := rs.ScopeSpans().AppendEmpty()
	siblingScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	sibling := siblingScope.Spans().AppendEmpty()
	sibling.SetName("plugin.chat")
	sibling.Attributes().PutStr("gen_ai.output.messages", "SENSITIVE")
	sibling.Events().AppendEmpty().SetName("gen_ai.user.message")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	findSpan(t, all, "invoke_agent pi")
	outSibling := findSpan(t, all, "plugin.chat")
	_, exists := outSibling.Attributes().Get("gen_ai.output.messages")
	require.False(t, exists, "content must not ride along on non-native scopes in a claimed group")
	require.Equal(t, 0, outSibling.Events().Len(), "content events are removed entirely")
	// The input keeps its content: raw fidelity is the raw pipeline's job.
	inSibling := rs.ScopeSpans().At(1).Spans().At(0)
	require.Equal(t, "SENSITIVE", attrString(t, inSibling, "gen_ai.output.messages"))
}

// TestPiTraceNormalizerKeepsDanglingParentsInChildOnlyBatch covers a batch
// carrying only canonical children: the chat-turn they ran under was exported
// in an earlier batch. Zeroing their parent would destroy the join key, so
// children keep their original parent span ID and a backend can reattach them
// once the chat-turn arrives, mirroring the Claude Code export behavior.
func TestPiTraceNormalizerKeepsDanglingParentsInChildOnlyBatch(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()
	turnParent := pcommon.SpanID{2}

	gen := spans.AppendEmpty()
	gen.SetName("llm-generation [main] [request]")
	gen.SetParentSpanID(turnParent)
	gen.Attributes().PutStr("model", "deepseek-v4-flash")
	tool := spans.AppendEmpty()
	tool.SetName("bash")
	tool.SetParentSpanID(turnParent)
	tool.Attributes().PutStr("toolName", "bash")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	require.Equal(t, turnParent, findSpan(t, all, "chat deepseek-v4-flash").ParentSpanID(),
		"a chat child in a batch without its chat-turn keeps its dangling parent")
	require.Equal(t, turnParent, findSpan(t, all, "execute_tool bash").ParentSpanID(),
		"a tool child in a batch without its chat-turn keeps its dangling parent")
}

// TestPiTraceNormalizerPreservesParentsAcrossBatches pins the split-export
// shape: pi exports the chat-turn in one batch and its children in the next,
// so the stateless connector sees two calls. Keeping the children's original
// parent span ID preserves the linkage a backend needs to reassemble the tree.
func TestPiTraceNormalizerPreservesParentsAcrossBatches(t *testing.T) {
	turnBatch := ptrace.NewTraces()
	scope := turnBatch.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	turn := scope.Spans().AppendEmpty()
	turn.SetName("chat-turn")
	turn.SetSpanID(pcommon.SpanID{2})
	turn.Attributes().PutStr("sessionId", "session-1")

	childrenBatch := ptrace.NewTraces()
	scope = childrenBatch.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	gen := scope.Spans().AppendEmpty()
	gen.SetName("llm-generation [main] [request]")
	gen.SetParentSpanID(pcommon.SpanID{2})
	gen.Attributes().PutStr("model", "deepseek-v4-flash")

	sink := &traceSink{}
	normalizer := New(sink)
	require.NoError(t, normalizer.ConsumeTraces(context.Background(), turnBatch))
	require.NoError(t, normalizer.ConsumeTraces(context.Background(), childrenBatch))

	all := reassemble(sink)
	root := findSpan(t, all, "invoke_agent pi")
	require.Equal(t, pcommon.SpanID{2}, root.SpanID())
	require.Equal(t, root.SpanID(), findSpan(t, all, "chat deepseek-v4-flash").ParentSpanID(),
		"children from a later batch keep the parent linkage for backend reassembly")
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

func attrInt(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	return value.Int()
}

// TestPiTraceNormalizerAgainstRealCapture pins the normalizer to a real
// @amaster.ai/pi-telemetry 0.1.9 wire capture (one tool-using turn on
// 2026-08-22: two agentic iterations and a bash tool call, captured by a local
// OTLP listener). Unlike hand-built inputs, this guards against upstream
// schema drift: if a future extension release renames its spans or attributes,
// this test fails and tells us to update the normalizer.
func TestPiTraceNormalizerAgainstRealCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/pi-native-traces.json")
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
	root := findSpan(t, all, "invoke_agent pi")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "the agent root carries no dangling parent")
	require.NotEmpty(t, attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "native", attrString(t, root, "telemetry.source"))

	chat := findSpan(t, all, "chat deepseek-v4-flash")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, int64(2286), attrInt(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(2334), attrInt(t, chat, "gen_ai.usage.total_tokens"))

	// Both generations in the capture pin the cache renames: the first reads
	// nothing from cache, the second reads 2304 tokens.
	var chats []ptrace.Span
	for i := 0; i < all.Len(); i++ {
		if span := all.At(i); span.Name() == "chat deepseek-v4-flash" {
			chats = append(chats, span)
		}
	}
	require.Len(t, chats, 2)
	require.Equal(t, int64(0), attrInt(t, chats[0], "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(2304), attrInt(t, chats[1], "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(7), attrInt(t, chats[1], "gen_ai.usage.output_tokens"))

	tool := findSpan(t, all, "execute_tool bash")
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, "bash", attrString(t, tool, "gen_ai.tool.name"))
	require.NotEmpty(t, attrString(t, tool, "gen_ai.tool.call.id"))
}
