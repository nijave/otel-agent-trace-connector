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
