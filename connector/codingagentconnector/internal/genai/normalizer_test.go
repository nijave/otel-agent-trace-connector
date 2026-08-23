// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package genai

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
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
		"github.copilot",
	} {
		input := ptrace.NewTraces()
		newGroup(input, scope, "chat glm-4.7")
		sink := &traceSink{}
		require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
		require.Len(t, sink.all(), 1, "scope %q must be claimed", scope)
	}
}

func TestGenAINormalizerClaimsCopilotScope(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "github-copilot")
	rs.Resource().Attributes().PutStr("service.version", "1.0.64")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("github.copilot")

	root := ss.Spans().AppendEmpty()
	root.SetName("invoke_agent")
	root.SetKind(ptrace.SpanKindInternal)
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.agent.name", "copilot-cli")
	root.Attributes().PutStr("gen_ai.conversation.id", "11111111-2222-3333-4444-555555555555")
	root.Attributes().PutInt("gen_ai.usage.input_tokens", 120)
	root.Attributes().PutInt("gen_ai.usage.output_tokens", 80)
	root.Attributes().PutDouble("github.copilot.cost", 0.15)
	root.Attributes().PutInt("github.copilot.aiu", 1)
	// Capture-gated content must never survive on claimed spans.
	root.Attributes().PutStr("gen_ai.system_instructions", "secret system prompt")

	chat := ss.Spans().AppendEmpty()
	chat.SetName("chat")
	chat.SetKind(ptrace.SpanKindClient)
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.request.model", "gpt-5.2")

	hook := ss.Spans().AppendEmpty()
	hook.SetName("execute_hook PreToolUse")
	hook.Attributes().PutStr("gen_ai.operation.name", "execute_hook")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	require.Equal(t, "invoke_agent copilot-cli", spans.At(0).Name())
	require.Equal(t, "chat gpt-5.2", spans.At(1).Name())
	// Operations outside the rename table keep their wire names.
	require.Equal(t, "execute_hook PreToolUse", spans.At(2).Name())

	attrs := spans.At(0).Attributes()
	require.Equal(t, "native", fixtureAttrString(t, attrs, "telemetry.source"))
	require.Equal(t, "github.copilot", fixtureAttrString(t, attrs, "coding_agent.source.scope"))
	require.Equal(t, "github-copilot", fixtureAttrString(t, attrs, "coding_agent.client.name"))
	require.Equal(t, "1.0.64", fixtureAttrString(t, attrs, "coding_agent.client.version"))
	cost, ok := attrs.Get("github.copilot.cost")
	require.True(t, ok, "vendor extras pass through untouched")
	require.Equal(t, 0.15, cost.Double())
	aiu, ok := attrs.Get("github.copilot.aiu")
	require.True(t, ok)
	require.Equal(t, int64(1), aiu.Int())
	_, ok = attrs.Get("gen_ai.system_instructions")
	require.False(t, ok, "capture-gated content must be stripped")

	// The input handed to the connector is never mutated.
	_, ok = input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().Get("gen_ai.system_instructions")
	require.True(t, ok)
}

func fixtureAttrString(t *testing.T, attrs pcommon.Map, key string) string {
	t.Helper()
	value, ok := attrs.Get(key)
	require.True(t, ok, "attribute %q missing", key)
	return value.Str()
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

func TestGenAINormalizerSkipsOpenCodeGroups(t *testing.T) {
	input := ptrace.NewTraces()
	// OpenCode group: the OpenCode normalizer owns it even when a GenAI scope
	// is present; claiming here would emit the group twice and leak raw ai.*
	// attributes into canonical output.
	opencodeGroup := input.ResourceSpans().AppendEmpty()
	opencodeScope := opencodeGroup.ScopeSpans().AppendEmpty()
	opencodeScope.Scope().SetName("opencode")
	opencodeScope.Spans().AppendEmpty().SetName("ai.streamText")
	genaiScope := opencodeGroup.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.util.genai.handler")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all(), "the OpenCode normalizer owns this group")
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
	// The input batch was not mutated by the provider mapping.
	inSpan := input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "openai", attrString(t, inSpan, "gen_ai.system"))
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

func TestGenAINormalizerPassesThroughInvokeWorkflow(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "opentelemetry.util.genai.handler", "invoke_workflow my-flow")
	span.Attributes().PutStr("gen_ai.operation.name", "invoke_workflow")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "invoke_workflow my-flow", out.Name(), "non-canonical operations keep their emitted names")
	require.Equal(t, "native", attrString(t, out, "telemetry.source"))
	require.Equal(t, "opentelemetry.util.genai.handler",
		attrString(t, out, "coding_agent.source.scope"))
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

// legacyContentAttributeKeys is the denylist the allowlist replaced, kept as
// a regression checklist: every key that once required manual denial — plus
// the older indexed prompt/completion layouts — must still fail to reach
// canonical output.
var legacyContentAttributeKeys = []string{
	"gen_ai.input.messages",
	"gen_ai.output.messages",
	"gen_ai.input.messages.ref",
	"gen_ai.output.messages.ref",
	"gen_ai.system_instructions",
	"system_prompt",
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
	"gen_ai.tool.definitions",
	"gen_ai.agent.tools",
	"gen_ai.user.message",
	"gen_ai.assistant.message",
	"gen_ai.system.message",
	"gen_ai.tool.message",
	"gen_ai.choice",
	"gen_ai.choice.message",
	"gen_ai.choice.tool.result",
	"gen_ai.prompt.0",
	"gen_ai.prompt.1",
	"gen_ai.completion.0",
	"gen_ai.completion.1.content",
}

func TestGenAINormalizerStripsContentAttributesAndEvents(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "strands.telemetry.tracer", "execute_tool get_marker")
	span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	span.Attributes().PutStr("gen_ai.tool.name", "get_marker")
	for _, key := range legacyContentAttributeKeys {
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
	// Unknown attributes on surviving events are stripped too.
	safeEvent.Attributes().PutStr("decision.rationale", "SENSITIVE")
	exceptionEvent := span.Events().AppendEmpty()
	exceptionEvent.SetName("exception")
	exceptionEvent.Attributes().PutStr("exception.type", "TimeoutError")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	for _, key := range legacyContentAttributeKeys {
		_, exists := out.Attributes().Get(key)
		require.False(t, exists, "content attribute %q survived", key)
	}
	require.Equal(t, 2, out.Events().Len(), "only non-content events survive")
	require.Equal(t, "gen_ai.tool.decision", out.Events().At(0).Name())
	require.Equal(t, 0, out.Events().At(0).Attributes().Len(), "unknown event attributes are stripped")
	require.Equal(t, "exception", out.Events().At(1).Name())
	require.Equal(t, "TimeoutError",
		fixtureAttrString(t, out.Events().At(1).Attributes(), "exception.type"),
		"allowlisted event attributes survive")
	// The input keeps its content: raw fidelity is the raw pipeline's job.
	require.Equal(t, 10, input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Events().Len())
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

// loadFixtureLines reads a testdata file captured from a live e2e run. The
// collector's file exporter writes one OTLP JSON export per line; merge the
// batches the way a trace backend would.
func loadFixtureLines(t *testing.T, path string) ptrace.Traces {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	unmarshaler := &ptrace.JSONUnmarshaler{}
	merged := ptrace.NewTraces()
	for scanner.Scan() {
		batch, err := unmarshaler.UnmarshalTraces(scanner.Bytes())
		require.NoError(t, err)
		batch.ResourceSpans().MoveAndAppendTo(merged.ResourceSpans())
	}
	require.NoError(t, scanner.Err())
	return merged
}

func eachFixtureSpan(t *testing.T, traces ptrace.Traces, visit func(ptrace.Span)) {
	t.Helper()
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				visit(spans.At(k))
			}
		}
	}
}

// fixtureSpansWithContentEvidence reports whether any span holds a content
// event, proving a raw capture is a real stripping input rather than an
// already-clean batch.
func fixtureSpansWithContentEvidence(traces ptrace.Traces) bool {
	found := false
	for i := 0; i < traces.ResourceSpans().Len() && !found; i++ {
		spansList := traces.ResourceSpans().At(i).ScopeSpans()
		for j := 0; j < spansList.Len() && !found; j++ {
			spans := spansList.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				events := spans.At(k).Events()
				for e := 0; e < events.Len(); e++ {
					if contentEventNames[events.At(e).Name()] {
						found = true
						break
					}
				}
			}
		}
	}
	return found
}

// TestGenAINormalizerProcessesCapturedRawFixtures runs the normalizer over
// sanitized OTLP captures from the live openai-adhoc and strands e2e stacks,
// guarding the handcrafted fixtures against schema drift in the real emitters.
func TestGenAINormalizerProcessesCapturedRawFixtures(t *testing.T) {
	for _, fixture := range []struct {
		name string
		// Strands exports prompt/completion content as span events by
		// default; openai-v2 default mode sends content to log events, which
		// never enter this edge.
		expectContentEvidence bool
	}{
		{name: "openai-adhoc"},
		{name: "strands", expectContentEvidence: true},
		{name: "copilot-cli"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			input := loadFixtureLines(t, filepath.Join("testdata", fixture.name+"-raw.otlp.json"))
			require.NotZero(t, input.SpanCount())
			require.Equal(t, fixture.expectContentEvidence, fixtureSpansWithContentEvidence(input),
				"raw capture content evidence must match the emitter's default behavior")

			sink := &traceSink{}
			require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
			outputs := sink.all()
			require.NotEmpty(t, outputs, "the fixture's scopes must be claimed")

			normalizedChat := 0
			for _, output := range outputs {
				eachFixtureSpan(t, output, func(span ptrace.Span) {
					for _, key := range legacyContentAttributeKeys {
						_, exists := span.Attributes().Get(key)
						require.False(t, exists, "content attribute %q survived", key)
					}
					for e := 0; e < span.Events().Len(); e++ {
						require.False(t, contentEventNames[span.Events().At(e).Name()],
							"content event %q survived", span.Events().At(e).Name())
					}
					if value, ok := span.Attributes().Get("telemetry.source"); ok && value.Str() == "native" {
						_, hasScope := span.Attributes().Get("coding_agent.source.scope")
						require.True(t, hasScope, "provenance scope missing on %q", span.Name())
					}
				})
				eachFixtureSpan(t, output, func(span ptrace.Span) {
					if value, ok := span.Attributes().Get("gen_ai.operation.name"); ok && value.Str() == "chat" {
						if usage, ok := span.Attributes().Get("gen_ai.usage.input_tokens"); ok && usage.Int() > 0 {
							normalizedChat++
						}
					}
				})
			}
			require.Positive(t, normalizedChat, "no normalized chat span with token usage")

			// The input keeps its content events: the copy handed downstream
			// never mutates the captured batch.
			require.Equal(t, fixture.expectContentEvidence, fixtureSpansWithContentEvidence(input))
		})
	}
}

// TestCapturedCanonicalFixturesHoldNoContent checks the canonical captures
// from the same live runs: the stripping contract held end to end in production
// wiring, not only under the unit fixtures.
func TestCapturedCanonicalFixturesHoldNoContent(t *testing.T) {
	for _, fixture := range []struct{ name string }{
		{name: "openai-adhoc"},
		{name: "strands"},
		{name: "copilot-cli"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			traces := loadFixtureLines(t, filepath.Join("testdata", fixture.name+"-canonical.otlp.json"))
			require.NotZero(t, traces.SpanCount())
			eachFixtureSpan(t, traces, func(span ptrace.Span) {
				for _, key := range legacyContentAttributeKeys {
					_, exists := span.Attributes().Get(key)
					require.False(t, exists, "content attribute %q in canonical capture", key)
				}
				for e := 0; e < span.Events().Len(); e++ {
					require.False(t, contentEventNames[span.Events().At(e).Name()],
						"content event %q in canonical capture", span.Events().At(e).Name())
				}
			})
		})
	}
}

func TestGenAINormalizerProcessesCapturedCopilotFixture(t *testing.T) {
	input := loadFixtureLines(t, filepath.Join("testdata", "copilot-native.otlp.json"))
	require.NotZero(t, input.SpanCount())

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	outputs := sink.all()
	require.Len(t, outputs, 1, "both fixture batches share the claimed scope")
	require.Equal(t, 2, outputs[0].ResourceSpans().Len(), "both flavors stay distinct resource groups")

	names := map[string]ptrace.Span{}
	eachFixtureSpan(t, outputs[0], func(span ptrace.Span) {
		for _, key := range legacyContentAttributeKeys {
			_, exists := span.Attributes().Get(key)
			require.False(t, exists, "content attribute %q survived on %q", key, span.Name())
		}
		names[span.Name()] = span
	})

	cliRoot, ok := names["invoke_agent copilot-cli"]
	require.True(t, ok, "CLI invoke_agent root renames by agent name")
	require.Equal(t, "native", fixtureAttrString(t, cliRoot.Attributes(), "telemetry.source"))
	require.Equal(t, "github.copilot", fixtureAttrString(t, cliRoot.Attributes(), "coding_agent.source.scope"))
	require.Equal(t, "github-copilot", fixtureAttrString(t, cliRoot.Attributes(), "coding_agent.client.name"))
	require.Equal(t, "11111111-2222-3333-4444-555555555555", fixtureAttrString(t, cliRoot.Attributes(), "gen_ai.conversation.id"))
	cost, ok := cliRoot.Attributes().Get("github.copilot.cost")
	require.True(t, ok, "vendor cost passes through")
	require.Equal(t, 0.15, cost.Double())
	var shutdownSeen bool
	for i := 0; i < cliRoot.Events().Len(); i++ {
		if cliRoot.Events().At(i).Name() == "github.copilot.session.shutdown" {
			shutdownSeen = true
		}
	}
	require.True(t, shutdownSeen, "lifecycle span events survive")

	chat, ok := names["chat gpt-5.2"]
	require.True(t, ok)
	require.Equal(t, "t-1", fixtureAttrString(t, chat.Attributes(), "github.copilot.turn_id"))

	tool, ok := names["execute_tool run_commands"]
	require.True(t, ok)
	require.Equal(t, "function", fixtureAttrString(t, tool.Attributes(), "gen_ai.tool.type"))

	vsCodeRoot, ok := names["invoke_agent copilotcli"]
	require.True(t, ok, "VS Code flavor renames by its own agent name")
	reasoning, ok := vsCodeRoot.Attributes().Get("gen_ai.usage.reasoning.output_tokens")
	require.True(t, ok, "the reasoning-token key passes through unmapped")
	require.Equal(t, int64(25), reasoning.Int())
	// Canonical output is an allowlist: repository URLs identify
	// infrastructure rather than carry operational signal, so both the
	// current and the legacy namespace keys fail closed.
	_, hasRepoURL := vsCodeRoot.Attributes().Get("github.copilot.git.repository")
	require.False(t, hasRepoURL, "repository URL must not reach canonical output")
	_, hasLegacyRepoURL := vsCodeRoot.Attributes().Get("copilot_chat.repo.remote_url")
	require.False(t, hasLegacyRepoURL, "legacy namespace must not ride along into canonical output")

	hook, ok := names["execute_hook PreToolUse"]
	require.True(t, ok, "unknown operations keep their wire names")
	require.Equal(t, "pass", fixtureAttrString(t, hook.Attributes(), "github.copilot.hook.decision"))
}
