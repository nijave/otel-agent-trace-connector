// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type routerSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*routerSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (s *routerSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := ptrace.NewTraces()
	traces.CopyTo(copied)
	s.traces = append(s.traces, copied)
	return nil
}

func TestTracesRouterSendsEachGroupToExactlyOneNormalizer(t *testing.T) {
	input := ptrace.NewTraces()

	claudeGroup := input.ResourceSpans().AppendEmpty()
	claudeGroup.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("claude_code.interaction")

	genaiGroup := input.ResourceSpans().AppendEmpty()
	genaiScope := genaiGroup.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	chat := genaiScope.Spans().AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")

	unknownGroup := input.ResourceSpans().AppendEmpty()
	unknownGroup.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("startup")

	sink := &routerSink{}
	router := newTracesRouter(sink)
	require.NoError(t, router.ConsumeTraces(context.Background(), input))

	names := map[string]int{}
	total := 0
	for _, traces := range sink.traces {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					names[spans.At(k).Name()]++
					total++
				}
			}
		}
	}
	require.Equal(t, 2, total, "unknown groups stay out of the canonical edge")
	require.Equal(t, 1, names["invoke_agent claude_code"], "claude normalizer claimed its group once")
	require.Equal(t, 1, names["chat glm-4.7"], "genai normalizer claimed its group once")
}
