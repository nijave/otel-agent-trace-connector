package pi

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	llm.Attributes().PutStr("llmGenerationId", "gen-1")
	llm.Attributes().PutStr("responseId", "router-abc123")
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	root := findSpan(t, spans, "invoke_agent pi")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(),
		"a parent absent from the batch must not survive; invoke_agent must be a real root")
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.Equal(t, "pi", attrString(t, root, "gen_ai.agent.name"))
	require.Equal(t, "session-1", attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "pi", attrString(t, root, "coding_agent.client.name"))
	require.Equal(t, "native", attrString(t, root, "coding_agent.source"))
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
	require.Equal(t, int64(0), attrInt(t, chat, "gen_ai.usage.cache_write.input_tokens"))
	// Stop reason and response id land under their canonical keys.
	require.Equal(t, "stop", attrStrSliceElem(t, chat, "gen_ai.response.finish_reasons"))
	require.Equal(t, "router-abc123", attrString(t, chat, "gen_ai.response.id"))

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
			"stopReason",
			"status",
			"model",
			"provider",
			"sessionId",
			"durationMs",
			"llmGenerationId",
			"responseId",
			"eventType",
			"toolName",
			"toolCallId",
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), byResource))
	require.Len(t, sink.all(), 1, "resource telemetry.sdk.name claims the group")

	other := ptrace.NewTraces()
	other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("codex.internal")
	sinkOther := &traceSink{}
	require.NoError(t, New(sinkOther, true).ConsumeTraces(context.Background(), other))
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
		require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	root := findSpan(t, all, "invoke_agent pi")
	normalizedTool := findSpan(t, all, "execute_tool bash")
	require.Equal(t, root.SpanID(), normalizedTool.ParentSpanID(),
		"orphaned tool span must attach to the agent root in the same batch")
	require.Equal(t, "bash", attrString(t, normalizedTool, "gen_ai.tool.name"))
	require.Equal(t, "execute_tool", attrString(t, normalizedTool, "gen_ai.operation.name"))
	require.Equal(t, "call_9c4debe87115427c83aa8826", attrString(t, normalizedTool, "gen_ai.tool.call.id"))
	for _, key := range []string{"toolName", "toolCallId", "sessionId", "status"} {
		_, ok := normalizedTool.Attributes().Get(key)
		require.False(t, ok, "%s must not reach canonical output", key)
	}
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	root := findSpan(t, all, "invoke_agent pi")
	require.Equal(t, root.SpanID(), findSpan(t, all, "chat").ParentSpanID(),
		"a chat child without a model suffix must attach to the agent root instead of dangling")
}

// TestPiTraceNormalizerDropsNonNativeSiblingsInClaimedGroups covers a claimed
// group that also carries a sibling instrumentation scope: the scope/sdk.name
// claim sweeps every instrumentation in the process into this edge, so
// non-native spans are dropped from canonical output while the input keeps
// them untouched.
func TestPiTraceNormalizerDropsNonNativeSiblingsInClaimedGroups(t *testing.T) {
	input, _ := piInput()
	rs := input.ResourceSpans().At(0)
	siblingScope := rs.ScopeSpans().AppendEmpty()
	siblingScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	sibling := siblingScope.Spans().AppendEmpty()
	sibling.SetName("plugin.chat")
	sibling.Attributes().PutStr("gen_ai.output.messages", "SENSITIVE")
	sibling.Events().AppendEmpty().SetName("gen_ai.user.message")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	findSpan(t, all, "invoke_agent pi")
	for i := 0; i < all.Len(); i++ {
		require.NotEqual(t, "plugin.chat", all.At(i).Name(),
			"non-native sibling spans must not reach canonical output")
	}
	// The input keeps the sibling span and its content: raw fidelity is the
	// raw pipeline's job.
	inSibling := rs.ScopeSpans().At(1).Spans().At(0)
	require.Equal(t, "plugin.chat", inSibling.Name())
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
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
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
	normalizer := New(sink, true)
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

// TestPiTraceNormalizerAllowlistsEverySpanType pins canonical attributes
// key-for-key: the wire carries far more than the edge maps, and a release that
// adds another key must not leak one. Each span type is checked with raw noise
// (including a made-up future key) on input.
func TestPiTraceNormalizerAllowlistsEverySpanType(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()

	turn := spans.AppendEmpty()
	turn.SetName("chat-turn")
	turn.SetSpanID(pcommon.SpanID{2})
	turn.Attributes().PutStr("sessionId", "session-1")
	turn.Attributes().PutStr("eventType", "chat_turn_completed")
	turn.Attributes().PutInt("promptNumber", 1)
	turn.Attributes().PutInt("durationMs", 1423)
	turn.Attributes().PutStr("langfuse.trace.name", "chat-turn")
	turn.Attributes().PutStr("futureWireKey", "leak")

	gen := spans.AppendEmpty()
	gen.SetName("llm-generation [main] [request]")
	gen.SetParentSpanID(pcommon.SpanID{2})
	gen.Attributes().PutStr("model", "opencode-go/claude-haiku-5-5")
	gen.Attributes().PutStr("requestedModel", "claude-haiku-5-5")
	gen.Attributes().PutStr("thinkingLevel", "high")
	gen.Attributes().PutStr("stopReason", "stop")
	gen.Attributes().PutStr("responseId", "resp-1")
	gen.Attributes().PutStr("api", "anthropic-messages")
	gen.Attributes().PutStr("status", "completed")
	gen.Attributes().PutStr("costSource", "model-pricing")
	gen.Attributes().PutStr("usage", `{"input":1,"output":2,"totalTokens":3,"cacheRead":4,"cacheWrite":5,"reasoning":6,"cacheWrite1h":0,"cost":{"total":0.5}}`)
	gen.Attributes().PutStr("langfuse.observation.output", "secret")
	gen.Attributes().PutStr("futureWireKey", "leak")

	tool := spans.AppendEmpty()
	tool.SetName("bash")
	tool.SetParentSpanID(pcommon.SpanID{2})
	tool.Attributes().PutStr("toolName", "bash")
	tool.Attributes().PutStr("toolCallId", "call-1")
	tool.Attributes().PutInt("durationMs", 11)
	tool.Attributes().PutStr("sessionId", "session-1")
	tool.Attributes().PutStr("futureWireKey", "leak")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)

	common := map[string]any{
		"coding_agent.source":      "native",
		"coding_agent.client.name": "pi",
	}
	withCommon := func(pairs map[string]any) map[string]any {
		attrs := make(map[string]any, len(pairs)+len(common))
		for key, value := range common {
			attrs[key] = value
		}
		for key, value := range pairs {
			attrs[key] = value
		}
		return attrs
	}

	require.Equal(t, withCommon(map[string]any{
		"gen_ai.operation.name":     "invoke_agent",
		"gen_ai.agent.name":         "pi",
		"gen_ai.conversation.id":    "session-1",
		"coding_agent.source.event": "chat-turn",
	}), findSpan(t, all, "invoke_agent pi").Attributes().AsRaw(), "chat-turn")

	require.Equal(t, withCommon(map[string]any{
		"gen_ai.operation.name":                 "chat",
		"gen_ai.request.model":                  "claude-haiku-5-5",
		"gen_ai.provider.name":                  "opencode-go",
		"gen_ai.request.reasoning.level":        "high",
		"gen_ai.response.finish_reasons":        []any{"stop"},
		"gen_ai.response.id":                    "resp-1",
		"gen_ai.usage.input_tokens":             int64(1),
		"gen_ai.usage.output_tokens":            int64(2),
		"gen_ai.usage.total_tokens":             int64(3),
		"gen_ai.usage.cache_read.input_tokens":  int64(4),
		"gen_ai.usage.cache_write.input_tokens": int64(5),
		"gen_ai.usage.reasoning.output_tokens":  int64(6),
		"coding_agent.usage.cost":               0.5,
		"coding_agent.source.event":             "llm-generation",
	}), findSpan(t, all, "chat claude-haiku-5-5").Attributes().AsRaw(), "llm-generation")

	require.Equal(t, withCommon(map[string]any{
		"gen_ai.operation.name":     "execute_tool",
		"gen_ai.tool.name":          "bash",
		"gen_ai.tool.call.id":       "call-1",
		"coding_agent.source.event": "bash",
	}), findSpan(t, all, "execute_tool bash").Attributes().AsRaw(), "tool")
}

// TestPiTraceNormalizerDropsLLMStreamSpans covers the span type 0.1.28 added
// for model output: it is not a canonical shape, so it drops (and takes the
// captured output content with it).
func TestPiTraceNormalizerDropsLLMStreamSpans(t *testing.T) {
	input := ptrace.NewTraces()
	scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	scope.Scope().SetName("@amaster.ai/pi-telemetry")
	spans := scope.Spans()
	turn := spans.AppendEmpty()
	turn.SetName("chat-turn")
	turn.SetSpanID(pcommon.SpanID{2})
	stream := spans.AppendEmpty()
	stream.SetName("llm-stream")
	stream.SetParentSpanID(pcommon.SpanID{2})
	stream.Attributes().PutStr("output.value", "SENSITIVE")
	stream.Attributes().PutStr("langfuse.observation.output", "SENSITIVE")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := reassemble(sink)
	findSpan(t, all, "invoke_agent pi")
	require.Equal(t, 1, all.Len(), "only the agent root survives")
	for i := 0; i < all.Len(); i++ {
		_, hasOutput := all.At(i).Attributes().Get("output.value")
		require.False(t, hasOutput, "model output content must not reach canonical output")
	}
}

// TestPiTraceNormalizerDerivesProviderFromModelPrefix pins the provider rules:
// the old wire's provider attribute wins verbatim, and the new wire's
// "<prefix>/<requestedModel>" model id reveals a provider only on an exact
// match — never by guessing.
func TestPiTraceNormalizerDerivesProviderFromModelPrefix(t *testing.T) {
	cases := []struct {
		name           string
		model          string
		requestedModel string
		provider       string
		wantModel      string
		wantProvider   string
	}{
		{
			name:           "exact prefix",
			model:          "opencode-go/claude-haiku-5-5",
			requestedModel: "claude-haiku-5-5",
			wantModel:      "claude-haiku-5-5",
			wantProvider:   "opencode-go",
		},
		{
			name:           "model does not match the requested model",
			model:          "opencode-go/claude-haiku-5-5",
			requestedModel: "claude-sonnet-5-5",
			wantModel:      "claude-sonnet-5-5",
			wantProvider:   "",
		},
		{
			name:         "model without a prefix",
			model:        "deepseek-v4-flash",
			wantModel:    "deepseek-v4-flash",
			wantProvider: "",
		},
		{
			name:         "prefixed model with no requested model",
			model:        "opencode-go/claude-haiku-5-5",
			wantModel:    "opencode-go/claude-haiku-5-5",
			wantProvider: "",
		},
		{
			name:         "provider attribute wins",
			model:        "deepseek-v4-flash",
			provider:     "opencode-go",
			wantModel:    "deepseek-v4-flash",
			wantProvider: "opencode-go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
			scope.Scope().SetName("@amaster.ai/pi-telemetry")
			gen := scope.Spans().AppendEmpty()
			gen.SetName("llm-generation [main] [request]")
			gen.Attributes().PutStr("model", tc.model)
			if tc.requestedModel != "" {
				gen.Attributes().PutStr("requestedModel", tc.requestedModel)
			}
			if tc.provider != "" {
				gen.Attributes().PutStr("provider", tc.provider)
			}

			sink := &traceSink{}
			require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
			chat := findSpan(t, reassemble(sink), "chat "+tc.wantModel)
			require.Equal(t, tc.wantModel, attrString(t, chat, "gen_ai.request.model"))
			provider, ok := chat.Attributes().Get("gen_ai.provider.name")
			if tc.wantProvider == "" {
				require.False(t, ok, "no provider may be guessed")
				return
			}
			require.True(t, ok)
			require.Equal(t, tc.wantProvider, provider.Str())
		})
	}
}

// TestPiTraceNormalizerMapsThinkingLevel pins the reasoning-level rename; the
// wire omits the key on models without a thinking level.
func TestPiTraceNormalizerMapsThinkingLevel(t *testing.T) {
	for _, tc := range []struct{ thinkingLevel, want string }{{thinkingLevel: "medium", want: "medium"}, {thinkingLevel: ""}} {
		input := ptrace.NewTraces()
		scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
		scope.Scope().SetName("@amaster.ai/pi-telemetry")
		gen := scope.Spans().AppendEmpty()
		gen.SetName("llm-generation [main] [request]")
		gen.Attributes().PutStr("model", "glm-5.3")
		if tc.thinkingLevel != "" {
			gen.Attributes().PutStr("thinkingLevel", tc.thinkingLevel)
		}

		sink := &traceSink{}
		require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
		chat := findSpan(t, reassemble(sink), "chat glm-5.3")
		level, ok := chat.Attributes().Get("gen_ai.request.reasoning.level")
		if tc.want == "" {
			require.False(t, ok, "an empty thinkingLevel maps no key")
			continue
		}
		require.True(t, ok)
		require.Equal(t, tc.want, level.Str())
	}
}

// TestPiTraceNormalizerDerivesTimeToFirstChunk pins the TTFT derivation: the
// 0.1.28 wire stamps langfuse.observation.completion_start_time with the first
// streamed token, so the canonical time to first chunk is that timestamp minus
// the span start. A timestamp the edge cannot turn into a non-negative
// duration maps nothing.
func TestPiTraceNormalizerDerivesTimeToFirstChunk(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start      string
		completion any // nil: the wire omits the key; other types land as that attribute type
		want       float64
		mapped     bool
	}{
		{
			name:       "present",
			start:      "2026-10-19T23:43:19.151Z",
			completion: "2026-10-19T23:43:19.794Z",
			want:       0.643,
			mapped:     true,
		},
		{
			name:  "absent",
			start: "2026-10-19T23:43:19.151Z",
		},
		{
			name:       "unparseable",
			start:      "2026-10-19T23:43:19.151Z",
			completion: "not-a-timestamp",
		},
		{
			name:       "not a string",
			start:      "2026-10-19T23:43:19.151Z",
			completion: 1792453399.794,
		},
		{
			name:       "before span start",
			start:      "2026-10-19T23:43:19.794Z",
			completion: "2026-10-19T23:43:19.151Z",
		},
		{
			name:       "level with span start",
			start:      "2026-10-19T23:43:19.794Z",
			completion: "2026-10-19T23:43:19.794Z",
			want:       0,
			mapped:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
			scope.Scope().SetName("@amaster.ai/pi-telemetry")
			gen := scope.Spans().AppendEmpty()
			gen.SetName("llm-generation [main] [request]")
			gen.Attributes().PutStr("model", "glm-5.3")
			start, err := time.Parse(time.RFC3339, tc.start)
			require.NoError(t, err)
			gen.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
			switch completion := tc.completion.(type) {
			case nil:
			case string:
				gen.Attributes().PutStr("langfuse.observation.completion_start_time", completion)
			case float64:
				gen.Attributes().PutDouble("langfuse.observation.completion_start_time", completion)
			}

			sink := &traceSink{}
			require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
			value, ok := findSpan(t, reassemble(sink), "chat glm-5.3").Attributes().Get("gen_ai.response.time_to_first_chunk")
			require.Equal(t, tc.mapped, ok, "canonical key presence")
			if !tc.mapped {
				return
			}
			require.Equal(t, pcommon.ValueTypeDouble, value.Type(), "time to first chunk is seconds as a double")
			require.InDelta(t, tc.want, value.Double(), 1e-9)
		})
	}
}

// TestPiTraceNormalizerMapsUsageBlobAndFallsBackToFlatKeys covers the two
// usage wires together: the serialized object wins per field, the flat keys
// cover fields the object omits, and a malformed object leaves the flat keys
// in charge.
func TestPiTraceNormalizerMapsUsageBlobAndFallsBackToFlatKeys(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		flat  map[string]string
		want  map[string]int64
		gone  []string
	}{
		{
			name:  "blob only",
			usage: `{"input":4,"output":56,"cacheRead":1227,"cacheWrite":0,"cacheWrite1h":0,"reasoning":9,"totalTokens":1287,"cost":{"total":0.00004}}`,
			want: map[string]int64{
				"gen_ai.usage.input_tokens":             4,
				"gen_ai.usage.output_tokens":            56,
				"gen_ai.usage.total_tokens":             1287,
				"gen_ai.usage.cache_read.input_tokens":  1227,
				"gen_ai.usage.cache_write.input_tokens": 0,
				"gen_ai.usage.reasoning.output_tokens":  9,
			},
		},
		{
			name:  "blob wins over flat keys, flat fills the gaps",
			usage: `{"input":7}`,
			flat: map[string]string{
				"usage.input":        "99",
				"usage.output":       "8",
				"usage.total_tokens": "15",
				"usage.cache_read":   "0",
				"usage.cache_write":  "6",
			},
			want: map[string]int64{
				"gen_ai.usage.input_tokens":             7,
				"gen_ai.usage.output_tokens":            8,
				"gen_ai.usage.total_tokens":             15,
				"gen_ai.usage.cache_read.input_tokens":  0,
				"gen_ai.usage.cache_write.input_tokens": 6,
			},
			gone: []string{"gen_ai.usage.reasoning.output_tokens"},
		},
		{
			name:  "malformed blob leaves the flat keys in charge",
			usage: `not json`,
			flat: map[string]string{
				"usage.input":        "3335",
				"usage.output":       "3",
				"usage.total_tokens": "3338",
			},
			want: map[string]int64{
				"gen_ai.usage.input_tokens":  3335,
				"gen_ai.usage.output_tokens": 3,
				"gen_ai.usage.total_tokens":  3338,
			},
			gone: []string{"gen_ai.usage.cache_read.input_tokens"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			scope := input.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
			scope.Scope().SetName("@amaster.ai/pi-telemetry")
			gen := scope.Spans().AppendEmpty()
			gen.SetName("llm-generation [main] [request]")
			gen.Attributes().PutStr("model", "glm-5.3")
			gen.Attributes().PutStr("usage", tc.usage)
			for key, value := range tc.flat {
				gen.Attributes().PutStr(key, value)
			}

			sink := &traceSink{}
			require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
			chat := findSpan(t, reassemble(sink), "chat glm-5.3")
			for key, want := range tc.want {
				require.Equal(t, want, attrInt(t, chat, key), key)
			}
			for _, key := range tc.gone {
				_, ok := chat.Attributes().Get(key)
				require.False(t, ok, "%s must be absent", key)
			}
			for _, key := range []string{"usage", "usage.cost.total", "cacheWrite1h"} {
				_, ok := chat.Attributes().Get(key)
				require.False(t, ok, "%s must not reach canonical output", key)
			}
		})
	}
}

// TestPiTraceNormalizerAgainstCaptureV019 pins the normalizer to a real
// @amaster.ai/pi-telemetry 0.1.9 wire capture (one tool-using turn on
// 2026-08-22: two agentic iterations and a bash tool call, captured by a local
// OTLP listener). This wire carries a flat usage.* attribute per counter and a
// provider attribute; it stays supported for older extension releases.
func TestPiTraceNormalizerAgainstCaptureV019(t *testing.T) {
	all := normalizeFixture(t, "testdata/pi-native-traces-0.1.9.json")

	root := findSpan(t, all, "invoke_agent pi")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "the agent root carries no dangling parent")
	require.NotEmpty(t, attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "native", attrString(t, root, "coding_agent.source"))

	chat := findSpan(t, all, "chat deepseek-v4-flash")
	require.Equal(t, root.SpanID(), chat.ParentSpanID())
	require.Equal(t, "opencode-go", attrString(t, chat, "gen_ai.provider.name"))
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

	// The capture's stop reasons and response IDs land under canonical keys:
	// the first generation stopped on a tool call, the second on "stop".
	require.Equal(t, "toolUse", attrStrSliceElem(t, chats[0], "gen_ai.response.finish_reasons"))
	require.Equal(t, "stop", attrStrSliceElem(t, chats[1], "gen_ai.response.finish_reasons"))
	require.NotEmpty(t, attrString(t, chats[0], "gen_ai.response.id"))
	require.NotEmpty(t, attrString(t, chats[1], "gen_ai.response.id"))

	// No raw wire key survives alongside its canonical counterpart.
	for _, span := range []ptrace.Span{root, chats[0], chats[1], tool} {
		for _, key := range []string{
			"model", "provider", "sessionId", "durationMs", "llmGenerationId",
			"responseId", "eventType", "toolName", "toolCallId", "status",
			"stopReason",
		} {
			_, ok := span.Attributes().Get(key)
			require.False(t, ok, "%s must not reach canonical output", key)
		}
	}
}

func attrStrSliceElem(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	return attrSlice(t, span, key).At(0).Str()
}

// TestPiTraceNormalizerAgainstCaptureV0128 pins the normalizer to a real
// @amaster.ai/pi-telemetry 0.1.28 wire capture (one tool-using turn on
// 2026-10-19, pi 1.3.0). That release dropped the provider attribute and the
// flat usage.* keys, prefixed model with the gateway, added thinkingLevel and
// a promptNumber, and introduced llm-stream spans.
func TestPiTraceNormalizerAgainstCaptureV0128(t *testing.T) {
	all := normalizeFixture(t, "testdata/pi-native-traces.json")

	root := findSpan(t, all, "invoke_agent pi")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "the agent root carries no dangling parent")
	require.Equal(t, "01a1568c-4a0b-766c-9f64-8056c3c86f9e", attrString(t, root, "gen_ai.conversation.id"))

	var chats []ptrace.Span
	for i := 0; i < all.Len(); i++ {
		if span := all.At(i); span.Name() == "chat claude-haiku-5-5" {
			chats = append(chats, span)
		}
	}
	require.Len(t, chats, 2, "the span name takes requestedModel, not the provider-prefixed model")
	for _, chat := range chats {
		require.Equal(t, root.SpanID(), chat.ParentSpanID())
		require.Equal(t, "claude-haiku-5-5", attrString(t, chat, "gen_ai.request.model"))
		require.Equal(t, "opencode-go", attrString(t, chat, "gen_ai.provider.name"),
			"the provider comes from the model's <prefix>/<requestedModel> shape")
		require.Equal(t, "medium", attrString(t, chat, "gen_ai.request.reasoning.level"))
		require.Equal(t, int64(0), attrInt(t, chat, "gen_ai.usage.reasoning.output_tokens"))
	}

	// Usage arrives only inside the serialized object on this wire.
	for i, want := range []struct{ input, output, total, cacheRead, cacheWrite int64 }{
		{input: 4, output: 56, total: 1287, cacheRead: 1227, cacheWrite: 0},
		{input: 2, output: 3, total: 1304, cacheRead: 1299, cacheWrite: 0},
	} {
		require.Equal(t, want.input, attrInt(t, chats[i], "gen_ai.usage.input_tokens"))
		require.Equal(t, want.output, attrInt(t, chats[i], "gen_ai.usage.output_tokens"))
		require.Equal(t, want.total, attrInt(t, chats[i], "gen_ai.usage.total_tokens"))
		require.Equal(t, want.cacheRead, attrInt(t, chats[i], "gen_ai.usage.cache_read.input_tokens"))
		require.Equal(t, want.cacheWrite, attrInt(t, chats[i], "gen_ai.usage.cache_write.input_tokens"))
	}
	require.Equal(t, "toolUse", attrStrSliceElem(t, chats[0], "gen_ai.response.finish_reasons"))
	require.Equal(t, "stop", attrStrSliceElem(t, chats[1], "gen_ai.response.finish_reasons"))
	require.Equal(t, "msg_011CgCUymZEdEn1F9FhtDGEW", attrString(t, chats[0], "gen_ai.response.id"))

	// Both generations carry completion_start_time, so both report a TTFT:
	// 23:43:19.794 minus the first span start, 23:43:20.550 minus the second.
	for i, want := range []float64{0.643, 0.650} {
		value, ok := chats[i].Attributes().Get("gen_ai.response.time_to_first_chunk")
		require.True(t, ok, "chat %d derives a time to first chunk", i)
		require.InDelta(t, want, value.Double(), 1e-9)
	}
	require.Equal(t, "msg_011CgCUyq5p9wHm8A4AMEZuK", attrString(t, chats[1], "gen_ai.response.id"))

	tool := findSpan(t, all, "execute_tool bash")
	require.Equal(t, root.SpanID(), tool.ParentSpanID())
	require.Equal(t, "bash", attrString(t, tool, "gen_ai.tool.name"))
	require.Equal(t, "toolu_01LUsBwN85yx7wHRRL4z1PuA", attrString(t, tool, "gen_ai.tool.call.id"))

	// llm-stream is not a canonical span type, and no raw key survives.
	for i := 0; i < all.Len(); i++ {
		span := all.At(i)
		require.NotEqual(t, "llm-stream", span.Name(), "llm-stream spans never reach canonical output")
		span.Attributes().Range(func(key string, _ pcommon.Value) bool {
			require.True(t, strings.HasPrefix(key, "gen_ai.") || strings.HasPrefix(key, "coding_agent."),
				"raw key %s must not reach canonical output on span %s", key, span.Name())
			return true
		})
	}
}

// normalizeFixture replays one JSONL OTLP capture through the normalizer and
// returns the reassembled canonical spans.
func normalizeFixture(t *testing.T, path string) ptrace.SpanSlice {
	t.Helper()
	data, err := os.ReadFile(path)
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
	return reassemble(sink)
}

func attrSlice(t *testing.T, span ptrace.Span, key string) pcommon.Slice {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok)
	return value.Slice()
}

func TestConsumeTracesFiltersResourceAttributes(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "pi-agent")
	rs.Resource().Attributes().PutStr("service.version", "1.0.0")
	rs.Resource().Attributes().PutStr("telemetry.sdk.name", "@amaster.ai/pi-telemetry")
	rs.Resource().Attributes().PutStr("vendor.thing", "x")
	root := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	root.SetName("chat-turn")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	attrs := sink.all()[0].ResourceSpans().At(0).Resource().Attributes()
	for _, key := range []string{"service.name", "service.version", "telemetry.sdk.name"} {
		value, ok := attrs.Get(key)
		require.True(t, ok, "canonical resource key %s must survive", key)
		require.NotEmpty(t, value.Str())
	}
	_, hasVendor := attrs.Get("vendor.thing")
	require.False(t, hasVendor, "vendor resource keys must not reach canonical output")
	normalizedRoot := findSpan(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "invoke_agent pi")
	require.Equal(t, "invoke_agent", attrString(t, normalizedRoot, "gen_ai.operation.name"))
}

// TestPiTraceNormalizerMapsCost pins coding_agent.usage.cost: the 0.1.28
// serialized usage object nests cost.total; the 0.1.9 wire carried a flat
// usage.cost.total. A missing or malformed cost maps nothing.
func TestPiTraceNormalizerMapsCost(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		flat  map[string]string
		want  float64
		found bool
	}{
		{name: "blob cost.total", usage: `{"input":4,"cost":{"input":0.0000004,"total":0.000181775}}`, want: 0.000181775, found: true},
		{name: "blob string total", usage: `{"input":4,"cost":{"total":"0.0001"}}`, want: 0.0001, found: true},
		{name: "blob zero total", usage: `{"input":4,"cost":{"total":0}}`, want: 0, found: true},
		{name: "flat fallback", usage: `{"input":4}`, flat: map[string]string{"usage.cost.total": "0.00002297"}, want: 0.00002297, found: true},
		{name: "blob wins over flat", usage: `{"cost":{"total":0.5}}`, flat: map[string]string{"usage.cost.total": "0.25"}, want: 0.5, found: true},
		{name: "malformed total", usage: `{"cost":{"total":"abc"}}`, found: false},
		{name: "no cost anywhere", usage: `{"input":4}`, found: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			rs := input.ResourceSpans().AppendEmpty()
			scope := rs.ScopeSpans().AppendEmpty()
			scope.Scope().SetName("@amaster.ai/pi-telemetry")
			llm := scope.Spans().AppendEmpty()
			llm.SetName("llm-generation [main] [request]")
			llm.Attributes().PutStr("requestedModel", "glm-5.3-flash")
			llm.Attributes().PutStr("usage", tc.usage)
			for k, v := range tc.flat {
				llm.Attributes().PutStr(k, v)
			}
			sink := &traceSink{}
			require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
			chat := findSpan(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "chat glm-5.3-flash")
			value, ok := chat.Attributes().Get("coding_agent.usage.cost")
			require.Equal(t, tc.found, ok)
			if tc.found {
				require.Equal(t, pcommon.ValueTypeDouble, value.Type())
				require.InDelta(t, tc.want, value.Double(), 1e-12)
			}
		})
	}
}
