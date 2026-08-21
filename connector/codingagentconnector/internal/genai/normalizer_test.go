// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package genai

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
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

// newGroup appends a resource group with one scope and one named span and
// returns the span for further decoration.
func newGroup(traces ptrace.Traces, scopeName, spanName string) ptrace.Span {
	rs := traces.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(scopeName)
	span := ss.Spans().AppendEmpty()
	span.SetName(spanName)
	return span
}

func TestGenAINormalizerClaimsKnownScopes(t *testing.T) {
	for _, scope := range []string{
		"opentelemetry.instrumentation.openai_v2",
		"opentelemetry.util.genai.handler",
		"opentelemetry.instrumentation.genai_openai",
		"strands.telemetry.tracer",
	} {
		input := ptrace.NewTraces()
		newGroup(input, scope, "chat glm-4.7")
		sink := &traceSink{}
		require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
		require.Len(t, sink.all(), 1, "scope %q must be claimed", scope)
	}
}

func TestGenAINormalizerSkipsUnknownScopesAndClaudeGroups(t *testing.T) {
	input := ptrace.NewTraces()
	// Unknown scope: some application tracer.
	newGroup(input, "my-app", "startup")
	// Claude group: the Claude normalizer owns it even when a GenAI scope is present.
	claudeGroup := input.ResourceSpans().AppendEmpty()
	claudeScope := claudeGroup.ScopeSpans().AppendEmpty()
	claudeScope.Scope().SetName("opentelemetry.util.genai.handler")
	claudeScope.Spans().AppendEmpty().SetName("claude_code.interaction")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all(), "no group may be claimed")
}

func TestGenAINormalizerKeepsWholeClaimedGroupAndInput(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	appScope := rs.ScopeSpans().AppendEmpty()
	appScope.Scope().SetName("my-agent")
	appScope.Spans().AppendEmpty().SetName("run")
	genaiScope := rs.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	output := sink.all()[0].ResourceSpans().At(0)
	require.Equal(t, 2, output.ScopeSpans().Len(), "application scope must survive")
	// The input is not mutated because Collector fan-out may send it elsewhere.
	require.Equal(t, "run", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name())
}

func TestGenAINormalizerNormalizesOpenAIV2LegacyChat(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "adhoc-agent")
	rs.Resource().Attributes().PutStr("service.version", "0.1.0")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	span := ss.Spans().AppendEmpty()
	span.SetName("chat glm-4.7")
	span.Attributes().PutStr("gen_ai.operation.name", "chat")
	span.Attributes().PutStr("gen_ai.system", "openai")
	span.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	span.Attributes().PutInt("gen_ai.usage.input_tokens", 12)
	span.Attributes().PutInt("gen_ai.usage.output_tokens", 34)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "chat glm-4.7", out.Name())
	require.Equal(t, "openai", attrString(t, out, "gen_ai.provider.name"))
	_, hasSystem := out.Attributes().Get("gen_ai.system")
	require.False(t, hasSystem, "legacy gen_ai.system must not survive")
	require.Equal(t, "native", attrString(t, out, "telemetry.source"))
	require.Equal(t, "opentelemetry.instrumentation.openai_v2",
		attrString(t, out, "coding_agent.source.scope"))
	require.Equal(t, "adhoc-agent", attrString(t, out, "coding_agent.client.name"))
	require.Equal(t, "0.1.0", attrString(t, out, "coding_agent.client.version"))
}

func TestGenAINormalizerKeepsExperimentalProviderName(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "opentelemetry.util.genai.handler", "chat glm-4.7")
	span.Attributes().PutStr("gen_ai.operation.name", "chat")
	span.Attributes().PutStr("gen_ai.provider.name", "openai")
	span.Attributes().PutStr("gen_ai.request.model", "glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "openai", attrString(t, out, "gen_ai.provider.name"))
}

func TestGenAINormalizerNormalizesStrandsTree(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("strands.telemetry.tracer")

	agent := ss.Spans().AppendEmpty()
	agent.SetName("invoke_agent strands-e2e")
	agent.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	agent.Attributes().PutStr("gen_ai.system", "strands-agents")
	agent.Attributes().PutStr("gen_ai.agent.name", "strands-e2e")

	cycle := ss.Spans().AppendEmpty()
	cycle.SetName("execute_event_loop_cycle")
	cycle.Attributes().PutStr("gen_ai.operation.name", "execute_event_loop_cycle")
	cycle.Attributes().PutStr("gen_ai.system", "strands-agents")

	chat := ss.Spans().AppendEmpty()
	chat.SetName("chat")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.system", "strands-agents")
	chat.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	chat.Attributes().PutInt("gen_ai.usage.prompt_tokens", 10)
	chat.Attributes().PutInt("gen_ai.usage.completion_tokens", 20)
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 10)
	chat.Attributes().PutInt("gen_ai.usage.output_tokens", 20)
	chat.Attributes().PutInt("gen_ai.usage.total_tokens", 30)

	tool := ss.Spans().AppendEmpty()
	tool.SetName("execute_tool get_marker")
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.system", "strands-agents")
	tool.Attributes().PutStr("gen_ai.tool.name", "get_marker")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	require.Equal(t, "invoke_agent strands-e2e", out.At(0).Name())
	require.Equal(t, "strands-agents", attrString(t, out.At(0), "gen_ai.provider.name"))
	// Non-canonical operations keep their emitted names.
	require.Equal(t, "execute_event_loop_cycle", out.At(1).Name())
	// Bare Strands chat gains the model suffix.
	require.Equal(t, "chat glm-4.7", out.At(2).Name())
	// Legacy token keys are mapped/removed; current keys and totals survive.
	_, hasPrompt := out.At(2).Attributes().Get("gen_ai.usage.prompt_tokens")
	require.False(t, hasPrompt)
	_, hasCompletion := out.At(2).Attributes().Get("gen_ai.usage.completion_tokens")
	require.False(t, hasCompletion)
	require.Equal(t, int64(10), attrInt(t, out.At(2), "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(20), attrInt(t, out.At(2), "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(30), attrInt(t, out.At(2), "gen_ai.usage.total_tokens"))
	require.Equal(t, "execute_tool get_marker", out.At(3).Name())
}

func TestGenAINormalizerMapsLegacyTokensWhenCurrentAbsent(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "strands.telemetry.tracer", "chat")
	span.Attributes().PutStr("gen_ai.operation.name", "chat")
	span.Attributes().PutInt("gen_ai.usage.prompt_tokens", 7)
	span.Attributes().PutInt("gen_ai.usage.completion_tokens", 9)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, int64(7), attrInt(t, out, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(9), attrInt(t, out, "gen_ai.usage.output_tokens"))
	// Model attribute is absent, so the emitted name stays.
	require.Equal(t, "chat", out.Name())
}

func TestGenAINormalizerLeavesSpansWithoutOperationName(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	appScope := rs.ScopeSpans().AppendEmpty()
	appScope.Scope().SetName("my-agent")
	appSpan := appScope.Spans().AppendEmpty()
	appSpan.SetName("run")
	genaiScope := rs.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	outApp := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "run", outApp.Name())
	_, tagged := outApp.Attributes().Get("telemetry.source")
	require.False(t, tagged, "spans outside matched scopes stay untouched")
}

func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on span %q", key, span.Name())
	return value.Str()
}

func attrInt(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on span %q", key, span.Name())
	return value.Int()
}

func TestGenAINormalizerStripsContentAttributesAndEvents(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "strands.telemetry.tracer", "execute_tool get_marker")
	span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	span.Attributes().PutStr("gen_ai.tool.name", "get_marker")
	for _, key := range []string{
		"gen_ai.input.messages", "gen_ai.output.messages",
		"gen_ai.input.messages.ref", "gen_ai.output.messages.ref",
		"gen_ai.system_instructions", "system_prompt",
		"gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
		"gen_ai.tool.definitions", "gen_ai.agent.tools",
		"gen_ai.user.message", "gen_ai.assistant.message",
		"gen_ai.system.message", "gen_ai.tool.message", "gen_ai.choice",
		"gen_ai.choice.message", "gen_ai.choice.tool.result",
	} {
		span.Attributes().PutStr(key, "SENSITIVE")
	}
	for _, name := range []string{
		"gen_ai.client.inference.operation.details",
		"gen_ai.user.message", "gen_ai.assistant.message",
		"gen_ai.system.message", "gen_ai.tool.message", "gen_ai.choice",
		"memory.query", "memory.content",
	} {
		event := span.Events().AppendEmpty()
		event.SetName(name)
		event.Attributes().PutStr("content", "SENSITIVE")
	}
	safeEvent := span.Events().AppendEmpty()
	safeEvent.SetName("gen_ai.tool.decision")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	for _, key := range contentAttributeKeys {
		_, exists := out.Attributes().Get(key)
		require.False(t, exists, "content attribute %q survived", key)
	}
	require.Equal(t, 1, out.Events().Len(), "only the non-content event survives")
	require.Equal(t, "gen_ai.tool.decision", out.Events().At(0).Name())
	// The input keeps its content: raw fidelity is the raw pipeline's job.
	require.Equal(t, 9, input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Events().Len())
}

func TestGenAINormalizerStripsContentFromUnmatchedScopesInClaimedGroups(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	appScope := rs.ScopeSpans().AppendEmpty()
	appScope.Scope().SetName("my-agent")
	appSpan := appScope.Spans().AppendEmpty()
	appSpan.SetName("run")
	appSpan.Attributes().PutStr("gen_ai.input.messages", "SENSITIVE")
	genaiScope := rs.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	outApp := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	_, exists := outApp.Attributes().Get("gen_ai.input.messages")
	require.False(t, exists, "content must not ride along on unmatched scopes")
}
