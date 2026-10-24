// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"context"
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

var (
	traceOne = pcommon.TraceID([16]byte{1})
	baseTime = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
)

// newCodexGroup builds one resource group the way codex_exec exports it: the
// scope name equals the resource service.name.
func newCodexGroup(input ptrace.Traces) ptrace.SpanSlice {
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex_exec")
	rs.Resource().Attributes().PutStr("service.version", "0.163.0")
	rs.Resource().Attributes().PutStr("env", "e2e")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("codex_exec")
	return ss.Spans()
}

func addSpan(spans ptrace.SpanSlice, name string, id byte, parent byte, start, end time.Duration) ptrace.Span {
	span := spans.AppendEmpty()
	span.SetName(name)
	span.SetTraceID(traceOne)
	span.SetSpanID(pcommon.SpanID([8]byte{id}))
	if parent != 0 {
		span.SetParentSpanID(pcommon.SpanID([8]byte{parent}))
	}
	span.SetKind(ptrace.SpanKindInternal)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(start)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(end)))
	// tracing-opentelemetry bookkeeping every exported span carries.
	span.Attributes().PutStr("code.file.path", "core/src/tasks/mod.rs")
	span.Attributes().PutInt("code.line.number", 355)
	span.Attributes().PutInt("busy_ns", 12)
	span.Attributes().PutInt("idle_ns", 34)
	span.Attributes().PutStr("thread.name", "tokio-runtime-worker")
	return span
}

func addTurn(spans ptrace.SpanSlice) ptrace.Span {
	turn := addSpan(spans, "session_task.turn", 1, 0, 0, 10*time.Second)
	turn.Attributes().PutStr("thread.id", "thread-0001")
	turn.Attributes().PutStr("turn.id", "turn-0001")
	turn.Attributes().PutStr("model", "gpt-5.6-luna")
	turn.Attributes().PutStr("codex.turn.reasoning_effort", "low")
	turn.Attributes().PutInt("codex.turn.token_usage.input_tokens", 100)
	turn.Attributes().PutInt("codex.turn.token_usage.cached_input_tokens", 40)
	turn.Attributes().PutInt("codex.turn.token_usage.cache_write_input_tokens", 7)
	turn.Attributes().PutInt("codex.turn.token_usage.non_cached_input_tokens", 60)
	turn.Attributes().PutInt("codex.turn.token_usage.output_tokens", 20)
	turn.Attributes().PutInt("codex.turn.token_usage.reasoning_output_tokens", 5)
	turn.Attributes().PutInt("codex.turn.token_usage.total_tokens", 120)
	return turn
}

func normalizeOne(t *testing.T, input ptrace.Traces) ptrace.SpanSlice {
	t.Helper()
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
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

func findSpan(t *testing.T, spans ptrace.SpanSlice, name string) ptrace.Span {
	t.Helper()
	for i := 0; i < spans.Len(); i++ {
		if spans.At(i).Name() == name {
			return spans.At(i)
		}
	}
	t.Fatalf("span %q not found", name)
	return ptrace.Span{}
}

func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on %s", key, span.Name())
	return value.Str()
}

func attrInt(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on %s", key, span.Name())
	return value.Int()
}

func requireOnlyCanonical(t *testing.T, span ptrace.Span) {
	t.Helper()
	span.Attributes().Range(func(key string, _ pcommon.Value) bool {
		require.NotContains(t, key, "codex.", "raw key %s on %s", key, span.Name())
		require.NotContains(t, key, "code.", "bookkeeping key %s on %s", key, span.Name())
		require.NotContains(t, key, "thread.", "bookkeeping key %s on %s", key, span.Name())
		require.NotEqual(t, "busy_ns", key)
		require.NotEqual(t, "idle_ns", key)
		require.NotEqual(t, "model", key)
		return true
	})
}

func TestContainsCodexSpansNeedsScopeAndMarker(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	require.True(t, ContainsCodexSpans(input.ResourceSpans().At(0)))

	noMarker := ptrace.NewTraces()
	addSpan(newCodexGroup(noMarker), "run_turn", 1, 0, 0, time.Second)
	require.False(t, ContainsCodexSpans(noMarker.ResourceSpans().At(0)), "bookkeeping spans alone do not claim")

	wrongScope := ptrace.NewTraces()
	rs := wrongScope.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "something-else")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("codex_exec")
	addTurn(ss.Spans())
	require.False(t, ContainsCodexSpans(wrongScope.ResourceSpans().At(0)), "the scope must equal the service name")

	dispatch := ptrace.NewTraces()
	addSpan(newCodexGroup(dispatch), "op.dispatch.user_turn", 1, 0, 0, time.Second)
	require.True(t, ContainsCodexSpans(dispatch.ResourceSpans().At(0)), "op.dispatch.* marks a Codex group too")

	// Codex exports a span when it ends, so a group often carries only the
	// response spans of a turn that has not closed yet; their codex.* usage
	// keys mark the group.
	responseOnly := ptrace.NewTraces()
	addResponse(newCodexGroup(responseOnly), "handle_responses", 3, 2)
	require.True(t, ContainsCodexSpans(responseOnly.ResourceSpans().At(0)), "codex.* attribute keys mark a Codex group too")

	toolOnly := ptrace.NewTraces()
	carrier := addSpan(newCodexGroup(toolOnly), "dispatch_tool_call_with_state", 5, 2, 0, time.Second)
	addToolResultEvent(carrier, true, "exec_command", "call-1", true)
	require.True(t, ContainsCodexSpans(toolOnly.ResourceSpans().At(0)), "a codex.tool_result event marks a Codex group too")

	genaiOnly := ptrace.NewTraces()
	foreign := addSpan(newCodexGroup(genaiOnly), "handle_responses", 3, 2, 0, time.Second)
	foreign.Attributes().PutInt("gen_ai.usage.input_tokens", 50)
	require.False(t, ContainsCodexSpans(genaiOnly.ResourceSpans().At(0)), "generic GenAI usage keys alone do not claim")
}

// TestResponseOnlyGroupStillEmitsChat pins Review Focus 1 end to end: a batch
// holding only a usage-bearing span (its turn exports later) still yields a
// chat span with its wire parent intact.
func TestResponseOnlyGroupStillEmitsChat(t *testing.T) {
	input := ptrace.NewTraces()
	addResponse(newCodexGroup(input), "handle_responses", 3, 2)
	out := normalizeOne(t, input)
	require.Equal(t, 1, out.Len())
	chat := findSpan(t, out, "chat")
	require.Equal(t, pcommon.SpanID([8]byte{2}), chat.ParentSpanID())
	require.Equal(t, int64(50), attrInt(t, chat, "gen_ai.usage.input_tokens"))
}

func TestTurnBecomesInvokeAgentRoot(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	turn := addTurn(spans)
	turn.SetParentSpanID(pcommon.SpanID([8]byte{9})) // op.dispatch.user_turn, absent from the batch

	out := normalizeOne(t, input)
	root := findSpan(t, out, "invoke_agent codex")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "the turn is re-rooted")
	require.Equal(t, traceOne, root.TraceID(), "Codex's trace id survives")
	require.Equal(t, pcommon.SpanID([8]byte{1}), root.SpanID())
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.Equal(t, "codex", attrString(t, root, "gen_ai.agent.name"))
	require.Equal(t, "openai", attrString(t, root, "gen_ai.provider.name"))
	require.Equal(t, "thread-0001", attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "gpt-5.6-luna", attrString(t, root, "gen_ai.request.model"))
	require.Equal(t, "low", attrString(t, root, "gen_ai.request.reasoning.level"))
	require.Equal(t, int64(100), attrInt(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(40), attrInt(t, root, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(7), attrInt(t, root, "gen_ai.usage.cache_write.input_tokens"))
	require.Equal(t, int64(20), attrInt(t, root, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(5), attrInt(t, root, "gen_ai.usage.reasoning.output_tokens"))
	require.Equal(t, int64(120), attrInt(t, root, "gen_ai.usage.total_tokens"))
	require.Equal(t, "native", attrString(t, root, "coding_agent.source"))
	require.Equal(t, "codex", attrString(t, root, "coding_agent.client.name"))
	require.Equal(t, "0.163.0", attrString(t, root, "coding_agent.client.version"))
	require.Equal(t, "session_task.turn", attrString(t, root, "coding_agent.source.event"))
	requireOnlyCanonical(t, root)
	_, hasNonCached := root.Attributes().Get("gen_ai.usage.non_cached_input_tokens")
	require.False(t, hasNonCached, "non_cached_input_tokens has no canonical key")
	require.Equal(t, "session_task.turn", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name(), "input is not mutated")
}

func TestCompactAndReviewTurnsAreRootsToo(t *testing.T) {
	for _, name := range []string{"session_task.compact", "session_task.review"} {
		input := ptrace.NewTraces()
		turn := addTurn(newCodexGroup(input))
		turn.SetName(name)
		root := findSpan(t, normalizeOne(t, input), "invoke_agent codex")
		require.Equal(t, name, attrString(t, root, "coding_agent.source.event"))
	}
}

func TestTurnWithoutUsageOrModelStillRoots(t *testing.T) {
	input := ptrace.NewTraces()
	turn := addSpan(newCodexGroup(input), "session_task.turn", 1, 0, 0, time.Second)
	turn.Attributes().PutStr("thread.id", "thread-0002")
	root := findSpan(t, normalizeOne(t, input), "invoke_agent codex")
	_, hasModel := root.Attributes().Get("gen_ai.request.model")
	require.False(t, hasModel)
	_, hasUsage := root.Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, hasUsage, "no usage on the wire, no usage keys")
}

func TestBookkeepingSpansDrop(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	addSpan(spans, "run_turn", 2, 1, time.Second, 9*time.Second)
	addSpan(spans, "responses.stream", 3, 2, time.Second, 8*time.Second)
	addSpan(spans, "codex.sampling", 4, 2, time.Second, 8*time.Second)
	out := normalizeOne(t, input)
	require.Equal(t, 1, out.Len(), "only the turn survives")
}

func addResponse(spans ptrace.SpanSlice, name string, id, parent byte) ptrace.Span {
	span := addSpan(spans, name, id, parent, 2*time.Second, 6*time.Second)
	span.Attributes().PutInt("gen_ai.usage.input_tokens", 50)
	span.Attributes().PutInt("gen_ai.usage.cache_read.input_tokens", 20)
	span.Attributes().PutInt("gen_ai.usage.cache_write.input_tokens", 3)
	span.Attributes().PutInt("gen_ai.usage.output_tokens", 10)
	span.Attributes().PutInt("codex.usage.reasoning_output_tokens", 2)
	span.Attributes().PutInt("codex.usage.total_tokens", 60)
	span.Attributes().PutStr("codex.request.reasoning_effort", "low")
	span.Attributes().PutStr("from", "output_item_done")
	return span
}

// TestResponseSpanBecomesChat pins the chat mapping: classification is by
// the gen_ai.usage.input_tokens attribute, whatever the wire name (Codex
// renames the span to the response event type), the model comes from the
// turn in the batch, and the codex.* usage spellings remap.
func TestResponseSpanBecomesChat(t *testing.T) {
	for _, wireName := range []string{"response.completed", "handle_responses"} {
		input := ptrace.NewTraces()
		spans := newCodexGroup(input)
		addTurn(spans)
		addSpan(spans, "run_turn", 2, 1, time.Second, 9*time.Second)
		addResponse(spans, wireName, 3, 2)

		out := normalizeOne(t, input)
		require.Equal(t, 2, out.Len())
		chat := findSpan(t, out, "chat gpt-5.6-luna")
		require.Equal(t, pcommon.SpanID([8]byte{1}), chat.ParentSpanID(), "run_turn dropped, so the chat reparents to the root")
		require.Equal(t, "chat", attrString(t, chat, "gen_ai.operation.name"))
		require.Equal(t, "openai", attrString(t, chat, "gen_ai.provider.name"))
		require.Equal(t, "gpt-5.6-luna", attrString(t, chat, "gen_ai.request.model"))
		require.Equal(t, "low", attrString(t, chat, "gen_ai.request.reasoning.level"))
		require.Equal(t, int64(50), attrInt(t, chat, "gen_ai.usage.input_tokens"))
		require.Equal(t, int64(20), attrInt(t, chat, "gen_ai.usage.cache_read.input_tokens"))
		require.Equal(t, int64(3), attrInt(t, chat, "gen_ai.usage.cache_write.input_tokens"))
		require.Equal(t, int64(10), attrInt(t, chat, "gen_ai.usage.output_tokens"))
		require.Equal(t, int64(2), attrInt(t, chat, "gen_ai.usage.reasoning.output_tokens"))
		require.Equal(t, int64(60), attrInt(t, chat, "gen_ai.usage.total_tokens"))
		require.Equal(t, wireName, attrString(t, chat, "coding_agent.source.event"))
		requireOnlyCanonical(t, chat)
		_, hasFrom := chat.Attributes().Get("from")
		require.False(t, hasFrom)
	}
}

func TestResponseWithoutTurnStaysBareChat(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	// No turn in the batch: still claimed because of the dispatch marker.
	addSpan(spans, "op.dispatch.user_turn", 7, 0, 0, 10*time.Second)
	addResponse(spans, "response.completed", 3, 7)
	out := normalizeOne(t, input)
	chat := findSpan(t, out, "chat")
	_, hasModel := chat.Attributes().Get("gen_ai.request.model")
	require.False(t, hasModel, "no turn, no model")
	require.Equal(t, pcommon.SpanID([8]byte{7}), chat.ParentSpanID(), "no root in the batch: the wire parent stays for the backend to reattach")
}

func TestResponseUsageCoercesStrings(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	span := addSpan(spans, "response.completed", 3, 1, 2*time.Second, 6*time.Second)
	span.Attributes().PutStr("gen_ai.usage.input_tokens", "50")
	span.Attributes().PutStr("codex.usage.total_tokens", "not-a-number")
	chat := findSpan(t, normalizeOne(t, input), "chat gpt-5.6-luna")
	require.Equal(t, int64(50), attrInt(t, chat, "gen_ai.usage.input_tokens"))
	_, hasTotal := chat.Attributes().Get("gen_ai.usage.total_tokens")
	require.False(t, hasTotal, "a malformed counter maps nothing")
}

func addToolResultEvent(span ptrace.Span, byEventAttr bool, toolName, callID string, success any) {
	event := span.Events().AppendEmpty()
	if byEventAttr {
		event.SetName("event otel/src/tool_result.rs:54")
		event.Attributes().PutStr("event.name", "codex.tool_result")
	} else {
		event.SetName("codex.tool_result")
	}
	event.SetTimestamp(span.EndTimestamp())
	event.Attributes().PutStr("tool_name", toolName)
	event.Attributes().PutStr("call_id", callID)
	event.Attributes().PutInt("duration_ms", 54)
	event.Attributes().PutInt("output_length", 120)
	switch v := success.(type) {
	case bool:
		event.Attributes().PutBool("success", v)
	case string:
		event.Attributes().PutStr("success", v)
	}
}

// TestToolResultEventBecomesExecuteTool pins the builtin tool mapping: the
// span carrying a codex.tool_result event (named directly or via its
// event.name attribute) becomes execute_tool, the event drops, and a false
// success (bool or string) marks the span failed.
func TestToolResultEventBecomesExecuteTool(t *testing.T) {
	cases := []struct {
		name        string
		byEventAttr bool
		success     any
		wantErr     bool
	}{
		{"named event, bool success", false, true, false},
		{"event.name attr, bool failure", true, false, true},
		{"string false", false, "false", true},
		{"string true", true, "true", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			spans := newCodexGroup(input)
			addTurn(spans)
			dispatch := addSpan(spans, "handle_tool_call", 5, 1, 3*time.Second, 4*time.Second)
			addToolResultEvent(dispatch, tc.byEventAttr, "exec_command", "call-1", tc.success)

			out := normalizeOne(t, input)
			tool := findSpan(t, out, "execute_tool exec_command")
			require.Equal(t, pcommon.SpanID([8]byte{1}), tool.ParentSpanID())
			require.Equal(t, "execute_tool", attrString(t, tool, "gen_ai.operation.name"))
			require.Equal(t, "exec_command", attrString(t, tool, "gen_ai.tool.name"))
			require.Equal(t, "call-1", attrString(t, tool, "gen_ai.tool.call.id"))
			require.Equal(t, "builtin", attrString(t, tool, "gen_ai.tool.type"))
			require.Equal(t, "handle_tool_call", attrString(t, tool, "coding_agent.source.event"))
			require.Equal(t, 0, tool.Events().Len(), "the tool_result event drops with its content lengths")
			if tc.wantErr {
				require.Equal(t, ptrace.StatusCodeError, tool.Status().Code())
				require.Equal(t, "tool execution failed", tool.Status().Message())
			} else {
				require.Equal(t, ptrace.StatusCodeUnset, tool.Status().Code())
			}
			requireOnlyCanonical(t, tool)
		})
	}
}

func TestMCPToolCallBecomesExecuteTool(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	mcp := addSpan(spans, "mcp.tools.call", 6, 1, 3*time.Second, 4*time.Second)
	mcp.SetKind(ptrace.SpanKindClient)
	mcp.Attributes().PutStr("tool.name", "search_issues")
	mcp.Attributes().PutStr("tool.call_id", "call-mcp-1")
	mcp.Attributes().PutStr("mcp.server.name", "github")
	mcp.Attributes().PutStr("conversation.id", "thread-0001")
	mcp.Attributes().PutStr("server.address", "mcp.example.test")
	mcp.Attributes().PutInt("server.port", 443)
	mcp.Attributes().PutStr("rpc.method", "tools/call")
	mcp.Status().SetCode(ptrace.StatusCodeError)
	mcp.Status().SetMessage("tool returned an error")

	tool := findSpan(t, normalizeOne(t, input), "execute_tool search_issues")
	require.Equal(t, ptrace.SpanKindClient, tool.Kind(), "kind passes through")
	require.Equal(t, "search_issues", attrString(t, tool, "gen_ai.tool.name"))
	require.Equal(t, "call-mcp-1", attrString(t, tool, "gen_ai.tool.call.id"))
	require.Equal(t, "mcp", attrString(t, tool, "gen_ai.tool.type"))
	require.Equal(t, "mcp.example.test", attrString(t, tool, "server.address"))
	require.Equal(t, int64(443), attrInt(t, tool, "server.port"))
	require.Equal(t, ptrace.StatusCodeError, tool.Status().Code(), "status passes through")
	_, hasServer := tool.Attributes().Get("mcp.server.name")
	require.False(t, hasServer)
	_, hasRPC := tool.Attributes().Get("rpc.method")
	require.False(t, hasRPC)
	requireOnlyCanonical(t, tool)
}

func TestToolSpanWithoutNameStaysBare(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	dispatch := addSpan(spans, "handle_tool_call", 5, 1, 3*time.Second, 4*time.Second)
	addToolResultEvent(dispatch, false, "", "", true)
	tool := findSpan(t, normalizeOne(t, input), "execute_tool")
	_, hasName := tool.Attributes().Get("gen_ai.tool.name")
	require.False(t, hasName)
	_, hasCall := tool.Attributes().Get("gen_ai.tool.call.id")
	require.False(t, hasCall)
}

// TestChildrenKeepParentsInTurnlessBatch pins the split-batch rule: Codex
// exports a child when it ends, so chat and tool spans can arrive before
// their turn. Without a root in the batch the edge leaves parent ids alone;
// the next batch's turn carries the same trace id and the backend reattaches.
func TestChildrenKeepParentsInTurnlessBatch(t *testing.T) {
	first := ptrace.NewTraces()
	spans := newCodexGroup(first)
	addSpan(spans, "op.dispatch.user_turn", 9, 0, 0, 10*time.Second)
	addResponse(spans, "response.completed", 3, 2)
	dispatch := addSpan(spans, "handle_tool_call", 5, 2, 3*time.Second, 4*time.Second)
	addToolResultEvent(dispatch, false, "exec_command", "call-1", true)
	out := normalizeOne(t, first)
	require.Equal(t, 2, out.Len())
	require.Equal(t, pcommon.SpanID([8]byte{2}), findSpan(t, out, "chat").ParentSpanID())
	require.Equal(t, pcommon.SpanID([8]byte{2}), findSpan(t, out, "execute_tool exec_command").ParentSpanID())

	second := ptrace.NewTraces()
	addTurn(newCodexGroup(second))
	root := findSpan(t, normalizeOne(t, second), "invoke_agent codex")
	require.Equal(t, traceOne, root.TraceID(), "same trace id joins the batches downstream")
}

func TestOrphansReparentToEarliestTurn(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	late := addTurn(spans)
	late.SetSpanID(pcommon.SpanID([8]byte{11}))
	late.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(20 * time.Second)))
	late.SetEndTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(30 * time.Second)))
	early := addTurn(spans)
	addResponse(spans, "response.completed", 3, 2)
	// A tool span Codex started under a detached task carries its own trace
	// id (seen in the 2026-10-23 capture); it must not be parented across
	// traces.
	detached := addSpan(spans, "dispatch_tool_call_with_state", 5, 4, 3*time.Second, 4*time.Second)
	detached.SetTraceID(pcommon.TraceID([16]byte{2}))
	addToolResultEvent(detached, true, "exec_command", "call-1", true)

	out := normalizeOne(t, input)
	require.Equal(t, early.SpanID(), findSpan(t, out, "chat gpt-5.6-luna").ParentSpanID())
	require.Equal(t, pcommon.SpanID([8]byte{4}), findSpan(t, out, "execute_tool exec_command").ParentSpanID(), "no root in its trace: the wire parent stays")
}

// TestOrphansReparentAcrossResourceGroups pins the batch-wide rule: Codex
// splits one export across resource groups, so the turn can land in a
// different group than the response and tool spans it parents.
func TestOrphansReparentAcrossResourceGroups(t *testing.T) {
	input := ptrace.NewTraces()
	children := newCodexGroup(input)
	addResponse(children, "handle_responses", 3, 2)
	carrier := addSpan(children, "dispatch_tool_call_with_state", 5, 4, 3*time.Second, 4*time.Second)
	addToolResultEvent(carrier, true, "exec_command", "call-1", true)
	turn := addTurn(newCodexGroup(input))

	out := normalizeOne(t, input)
	require.Equal(t, 3, out.Len())
	require.Equal(t, turn.SpanID(), findSpan(t, out, "chat gpt-5.6-luna").ParentSpanID(), "the chat finds its turn in the other group")
	require.Equal(t, turn.SpanID(), findSpan(t, out, "execute_tool exec_command").ParentSpanID())
	require.Equal(t, "gpt-5.6-luna", attrString(t, findSpan(t, out, "chat gpt-5.6-luna"), "gen_ai.request.model"))
}

func TestResourceIsFiltered(t *testing.T) {
	input := ptrace.NewTraces()
	addTurn(newCodexGroup(input))
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	resource := sink.all()[0].ResourceSpans().At(0).Resource().Attributes()
	_, hasEnv := resource.Get("env")
	require.False(t, hasEnv, "non-canonical resource keys drop")
	_, hasService := resource.Get("service.name")
	require.True(t, hasService)
}
