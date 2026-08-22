// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"
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
	require.Equal(t, "native", attrString(span, "telemetry.source"))
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

func TestNormalizerEmitsNothingWithoutClaimedGroups(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "com.opencode", "opencode.llm")
	newGroup(input, "my-app", "run")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all())
}
