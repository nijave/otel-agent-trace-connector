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

	piGroup := input.ResourceSpans().AppendEmpty()
	piScope := piGroup.ScopeSpans().AppendEmpty()
	piScope.Scope().SetName("@amaster.ai/pi-telemetry")
	piScope.Spans().AppendEmpty().SetName("chat-turn")

	unknownGroup := input.ResourceSpans().AppendEmpty()
	unknownGroup.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("startup")

	opencodeGroup := input.ResourceSpans().AppendEmpty()
	opencodeScope := opencodeGroup.ScopeSpans().AppendEmpty()
	opencodeScope.Scope().SetName("opencode")
	step := opencodeScope.Spans().AppendEmpty()
	step.SetName("ai.streamText")
	step.Attributes().PutStr("session.id", "ses_router")

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
	require.Equal(t, 4, total, "unknown groups stay out of the canonical edge")
	require.Equal(t, 1, names["invoke_agent claude_code"], "claude normalizer claimed its group once")
	require.Equal(t, 1, names["chat glm-4.7"], "genai normalizer claimed its group once")
	require.Equal(t, 1, names["invoke_agent opencode"], "opencode normalizer claimed its group once")
	require.Equal(t, 1, names["invoke_agent pi"], "pi normalizer claimed its group once")
}

func TestTracesRouterEmitsMixedPiGenAIGroupOnce(t *testing.T) {
	input := ptrace.NewTraces()
	// One group carrying both a Pi scope and a GenAI-semconv scope (an agent
	// SDK instrumented inside a Pi-extension process): only the Pi normalizer
	// may claim it.
	mixedGroup := input.ResourceSpans().AppendEmpty()
	piScope := mixedGroup.ScopeSpans().AppendEmpty()
	piScope.Scope().SetName("@amaster.ai/pi-telemetry")
	piScope.Spans().AppendEmpty().SetName("chat-turn")
	genaiScope := mixedGroup.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	chat := genaiScope.Spans().AppendEmpty()
	chat.SetName("chat gpt-5.2")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")

	sink := &routerSink{}
	router := newTracesRouter(sink)
	require.NoError(t, router.ConsumeTraces(context.Background(), input))

	names := map[string]int{}
	for _, traces := range sink.traces {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					names[spans.At(k).Name()]++
				}
			}
		}
	}
	require.Equal(t, 1, names["invoke_agent pi"], "pi normalizer claimed the group once")
	require.Equal(t, 1, names["chat gpt-5.2"], "the genai edge must defer Pi-owned groups")
}
