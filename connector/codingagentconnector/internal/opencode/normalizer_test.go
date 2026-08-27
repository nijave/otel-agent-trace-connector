// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestContainsOpenCodeSpansExactScopeOnly(t *testing.T) {
	input := ptrace.NewTraces()
	require.False(t, ContainsOpenCodeSpans(input.ResourceSpans().AppendEmpty()))

	exact := input.ResourceSpans().AppendEmpty()
	exact.ScopeSpans().AppendEmpty().Scope().SetName("opencode")
	require.True(t, ContainsOpenCodeSpans(exact))

	plugin := input.ResourceSpans().AppendEmpty()
	plugin.ScopeSpans().AppendEmpty().Scope().SetName("opencode.plugins")
	require.False(t, ContainsOpenCodeSpans(plugin), "prefix matches belong to plugins/Kilo")

	kilo := input.ResourceSpans().AppendEmpty()
	kilo.ScopeSpans().AppendEmpty().Scope().SetName("com.opencode")
	require.False(t, ContainsOpenCodeSpans(kilo))
}

func TestNormalizerClaimsGroupDropsNoiseAndKeepsIdentity(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "opencode")
	rs.Resource().Attributes().PutStr("service.version", "1.18.21")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")

	root := ss.Spans().AppendEmpty()
	root.SetName("ai.streamText")
	root.SetTraceID([16]byte{1})
	root.SetSpanID([8]byte{2})
	root.SetParentSpanID(pcommon.SpanID{9})
	root.SetKind(ptrace.SpanKindInternal)
	root.Status().SetCode(ptrace.StatusCodeError)
	root.Status().SetMessage("boom")
	root.Attributes().PutStr("session.id", "ses_abc")
	root.Attributes().PutInt("ai.usage.inputTokens", 1000)
	root.Attributes().PutInt("ai.usage.outputTokens", 50)
	root.Attributes().PutInt("ai.usage.cachedInputTokens", 400)
	root.Attributes().PutStr("ai.response.text", "SECRET COMPLETION")

	effect := ss.Spans().AppendEmpty()
	effect.SetName("sql.execute")

	otherScope := rs.ScopeSpans().AppendEmpty()
	otherScope.Scope().SetName("some-lib")
	otherScope.Spans().AppendEmpty().SetName("http.client")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)

	outRS := sink.all()[0].ResourceSpans().At(0)
	var out []ptrace.Span
	for i := 0; i < outRS.ScopeSpans().Len(); i++ {
		spans := outRS.ScopeSpans().At(i).Spans()
		for j := 0; j < spans.Len(); j++ {
			out = append(out, spans.At(j))
		}
	}
	require.Len(t, out, 1, "Effect and non-opencode-scope spans must be dropped")

	span := out[0]
	require.Equal(t, "invoke_agent opencode", span.Name())
	require.Equal(t, pcommon.TraceID{1}, span.TraceID())
	require.Equal(t, pcommon.SpanID{2}, span.SpanID())
	require.Equal(t, pcommon.SpanID{}, span.ParentSpanID(), "canonical roots must be re-rooted")
	require.Equal(t, ptrace.SpanKindInternal, span.Kind())
	require.Equal(t, ptrace.StatusCodeError, span.Status().Code())
	require.Equal(t, "boom", span.Status().Message())
	attrString := func(s ptrace.Span, key string) string {
		v, ok := s.Attributes().Get(key)
		require.True(t, ok, "%s must be present", key)
		return v.Str()
	}
	require.Equal(t, "ses_abc", attrString(span, "gen_ai.conversation.id"))
	require.Equal(t, "invoke_agent", attrString(span, "gen_ai.operation.name"))
	require.Equal(t, "opencode", attrString(span, "gen_ai.agent.name"))
	require.Equal(t, "native", attrString(span, "coding_agent.source"))
	require.Equal(t, "opencode", attrString(span, "coding_agent.client.name"))
	require.Equal(t, "1.18.21", attrString(span, "coding_agent.client.version"))
	require.Equal(t, "ai.streamText", attrString(span, "coding_agent.source.event"))

	usageInt := func(s ptrace.Span, key string) int64 {
		v, ok := s.Attributes().Get(key)
		require.True(t, ok, "%s must be present", key)
		return v.Int()
	}
	require.Equal(t, int64(1000), usageInt(span, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(50), usageInt(span, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(400), usageInt(span, "gen_ai.usage.cache_read.input_tokens"))

	for _, forbidden := range []string{"ai.response.text", "ai.usage.inputTokens", "session.id"} {
		_, ok := span.Attributes().Get(forbidden)
		require.False(t, ok, "%s must not reach canonical output", forbidden)
	}

	require.Equal(t, "sql.execute", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(1).Name(),
		"input must not be mutated")
}

func TestNormalizerFallsBackToResourceSessionID(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("session.id", "ses_resource")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")
	ss.Spans().AppendEmpty().SetName("ai.streamText")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	v, ok := out.Attributes().Get("gen_ai.conversation.id")
	require.True(t, ok)
	require.Equal(t, "ses_resource", v.Str())
}

func TestNormalizerMissingUsageEmitsWithoutTokens(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "opencode", "ai.streamText")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	_, hasInput := out.Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, hasInput, "missing wire usage must stay absent, not zero-filled")
}

// TestNormalizerConvertsTTFTToSeconds pins the canonical TTFT unit: the wire
// carries fractional milliseconds and canonical output stores fractional
// seconds as a double.
func TestNormalizerConvertsTTFTToSeconds(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "opencode", "ai.streamText.doStream")
	span.Attributes().PutDouble("ai.response.msToFirstChunk", 250)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	value, ok := out.Attributes().Get("gen_ai.response.time_to_first_chunk")
	require.True(t, ok)
	require.Equal(t, pcommon.ValueTypeDouble, value.Type())
	require.InDelta(t, 0.25, value.Double(), 1e-9)
}

func TestNormalizerEmitsNothingWithoutClaimedGroups(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "com.opencode", "opencode.llm")
	newGroup(input, "my-app", "run")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all())
}

func TestNormalizerRenamesDoStreamAndToolCall(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")

	doStream := ss.Spans().AppendEmpty()
	doStream.SetName("ai.streamText.doStream")
	doStream.SetSpanID([8]byte{3})
	doStream.SetParentSpanID([8]byte{2})
	doStream.Attributes().PutStr("gen_ai.request.model", "ox-alpha-free")
	doStream.Attributes().PutStr("gen_ai.response.id", "resp_1")
	doStream.Attributes().PutStr("gen_ai.response.finish_reasons", "stop")

	tool := ss.Spans().AppendEmpty()
	tool.SetName("ai.toolCall")
	tool.SetSpanID([8]byte{4})
	tool.SetParentSpanID([8]byte{2})
	tool.Attributes().PutStr("ai.toolCall.name", "bash")
	tool.Attributes().PutStr("ai.toolCall.id", "call_1")
	// An empty wire provider must stay absent, not become an empty canonical one.
	tool.Attributes().PutStr("ai.model.provider", "")
	tool.Attributes().PutStr("ai.toolCall.args", "SECRET ARGS")
	tool.Attributes().PutStr("ai.toolCall.result", "SECRET RESULT")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	require.Equal(t, "chat ox-alpha-free", spans.At(0).Name())
	v, ok := spans.At(0).Attributes().Get("gen_ai.operation.name")
	require.True(t, ok)
	require.Equal(t, "chat", v.Str())
	_, leaked := spans.At(0).Attributes().Get("gen_ai.response.id")
	require.False(t, leaked, "response metadata beyond model stays off canonical chat")

	require.Equal(t, "execute_tool bash", spans.At(1).Name())
	toolAttrs := spans.At(1).Attributes()
	name, ok := toolAttrs.Get("gen_ai.tool.name")
	require.True(t, ok)
	require.Equal(t, "bash", name.Str())
	_, hasProvider := toolAttrs.Get("gen_ai.provider.name")
	require.False(t, hasProvider, "an empty wire provider must not be copied")
	op, ok := toolAttrs.Get("gen_ai.operation.name")
	require.True(t, ok)
	require.Equal(t, "execute_tool", op.Str())
	for _, secret := range []string{"ai.toolCall.args", "ai.toolCall.result", "ai.toolCall.id"} {
		_, leaked := toolAttrs.Get(secret)
		require.False(t, leaked, "%s must not reach canonical output", secret)
	}
	require.Equal(t, pcommon.SpanID{3}, spans.At(0).SpanID(), "IDs pass through for backend reassembly")
	require.Equal(t, pcommon.SpanID{4}, spans.At(1).SpanID())
}

func TestNormalizerBareNamesWhenSubjectMissing(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "opencode", "ai.streamText.doStream")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "chat", out.Name())

	input2 := ptrace.NewTraces()
	newGroup(input2, "opencode", "ai.toolCall")
	sink2 := &traceSink{}
	require.NoError(t, New(sink2).ConsumeTraces(context.Background(), input2))
	out2 := sink2.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "execute_tool", out2.Name())
}

func countSpans(traces ptrace.Traces) int {
	total := 0
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			total += rs.ScopeSpans().At(j).Spans().Len()
		}
	}
	return total
}

func anySpanNameWithPrefix(names map[string]bool, prefix string) bool {
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func stringAttrOf(span ptrace.Span, key string) string {
	value, ok := span.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

func TestNormalizerFixtureReplay(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "opencode-native-traces.json"))
	require.NoError(t, err, "run scripts/e2e-opencode.sh and capture the fixture first")
	unmarshaler := &ptrace.JSONUnmarshaler{}
	input, err := unmarshaler.UnmarshalTraces(data)
	require.NoError(t, err)
	inputSpans := countSpans(input)
	require.Positive(t, inputSpans)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	output := sink.all()[0]
	require.Less(t, countSpans(output), inputSpans, "Effect noise must be dropped")

	names := map[string]bool{}
	roots := 0
	for i := 0; i < output.ResourceSpans().Len(); i++ {
		rs := output.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				names[span.Name()] = true
				for _, secret := range []string{"ai.response.text", "ai.toolCall.args", "ai.toolCall.result", "ai.model.provider"} {
					_, leaked := span.Attributes().Get(secret)
					require.False(t, leaked, "%s must not survive normalization", secret)
				}
				switch stringAttrOf(span, "gen_ai.operation.name") {
				case "invoke_agent", "chat":
					require.Equal(t, "oclaude.chat", stringAttrOf(span, "gen_ai.provider.name"),
						"the wire names the provider on every model-call span")
				case "execute_tool":
					_, hasProvider := span.Attributes().Get("gen_ai.provider.name")
					require.False(t, hasProvider, "tool spans carry no wire provider")
				}
				if strings.HasPrefix(span.Name(), "invoke_agent") {
					roots++
				}
			}
		}
	}
	for name := range names {
		switch strings.SplitN(name, " ", 2)[0] {
		case "invoke_agent", "chat", "execute_tool":
		default:
			t.Fatalf("unexpected canonical span name %q", name)
		}
	}
	require.Positive(t, roots, "fixture must contain at least one step root")
	require.True(t, anySpanNameWithPrefix(names, "chat"), "fixture must contain a chat child")
	require.True(t, anySpanNameWithPrefix(names, "execute_tool"), "fixture must contain an execute_tool child")
}

func TestNormalizerFixtureReplayChatCarriesUsage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "opencode-native-traces.json"))
	require.NoError(t, err)
	input, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	require.NoError(t, err)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)

	var chat *ptrace.Span
	out := sink.all()[0]
	for i := 0; i < out.ResourceSpans().Len() && chat == nil; i++ {
		rs := out.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len() && chat == nil; j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				if strings.HasPrefix(spans.At(k).Name(), "chat") {
					s := spans.At(k)
					chat = &s
					break
				}
			}
		}
	}
	require.NotNil(t, chat, "fixture must contain a chat span")

	usageInt := func(key string) int64 {
		v, ok := chat.Attributes().Get(key)
		require.True(t, ok, "chat span must carry %s", key)
		return v.Int()
	}
	require.Equal(t, int64(7136), usageInt("gen_ai.usage.input_tokens"))
	require.Equal(t, int64(48), usageInt("gen_ai.usage.output_tokens"))
	require.Equal(t, int64(7184), usageInt("gen_ai.usage.total_tokens"))
	require.Equal(t, int64(7), usageInt("gen_ai.usage.reasoning.output_tokens"))
	// Canonical TTFT is fractional seconds: 3276.662537 ms on the wire.
	ttft, ok := chat.Attributes().Get("gen_ai.response.time_to_first_chunk")
	require.True(t, ok)
	require.Equal(t, pcommon.ValueTypeDouble, ttft.Type())
	require.InDelta(t, 3.276662537, ttft.Double(), 1e-9)

	for _, dropped := range []string{
		"ai.usage.totalTokens",
		"ai.usage.reasoningTokens",
		"ai.usage.outputTokenDetails.reasoningTokens",
		"ai.response.msToFirstChunk",
	} {
		_, leaked := chat.Attributes().Get(dropped)
		require.False(t, leaked, "%s must not reach canonical output", dropped)
	}
}

func TestNormalizerMapsRootReasoningTokens(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "opencode")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")
	root := ss.Spans().AppendEmpty()
	root.SetName("ai.streamText")
	root.Attributes().PutStr("session.id", "ses_r")
	root.Attributes().PutInt("ai.usage.reasoningTokens", 17)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	got, ok := out.Attributes().Get("gen_ai.usage.reasoning.output_tokens")
	require.True(t, ok, "root reasoning tokens must map")
	require.Equal(t, int64(17), got.Int())
}

func TestConsumeTracesFiltersResourceAttributes(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "opencode")
	rs.Resource().Attributes().PutStr("service.version", "1.18.21")
	rs.Resource().Attributes().PutStr("session.id", "session-1")
	rs.Resource().Attributes().PutStr("vendor.thing", "x")
	root := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	rs.ScopeSpans().At(0).Scope().SetName("opencode")
	root.SetName("ai.streamText")
	root.SetTraceID([16]byte{1})
	root.SetSpanID([8]byte{2})
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
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
}
