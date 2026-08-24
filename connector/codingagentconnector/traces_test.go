// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

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
	require.Equal(t, 0, names["chat gpt-5.2"],
		"the genai edge defers Pi-owned groups and pi drops non-native sibling scopes")
}

func TestTracesRouterEmitsMixedOpenHandsGenAIGroupOnce(t *testing.T) {
	input := ptrace.NewTraces()
	// One group carrying both an OpenHands lmnr.tracer scope and a
	// GenAI-semconv scope (an agent SDK instrumented inside an OpenHands
	// process): only the OpenHands normalizer may claim it.
	mixedGroup := input.ResourceSpans().AppendEmpty()
	lmnrScope := mixedGroup.ScopeSpans().AppendEmpty()
	lmnrScope.Scope().SetName("lmnr.tracer")
	root := lmnrScope.Spans().AppendEmpty()
	root.SetTraceID(pcommon.TraceID([16]byte{3}))
	root.SetSpanID(pcommon.SpanID([8]byte{4}))
	root.SetName("conversation")
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 5, 0, time.UTC)))
	llm := lmnrScope.Spans().AppendEmpty()
	llm.SetTraceID(pcommon.TraceID([16]byte{3}))
	llm.SetSpanID(pcommon.SpanID([8]byte{5}))
	llm.SetParentSpanID(root.SpanID())
	llm.SetName("llm-call")
	llm.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 1, 0, time.UTC)))
	llm.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 2, 0, time.UTC)))
	llm.Attributes().PutStr("lmnr.span.type", "LLM")
	llm.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
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
	require.Equal(t, 1, names["invoke_agent openhands"], "openhands normalizer claimed the group once")
	require.Equal(t, 1, names["chat glm-4.7"], "the openhands edge emitted its chat span once")
	require.Zero(t, names["chat gpt-5.2"], "the genai edge must defer OpenHands-owned groups")
}

func TestTracesRouterClaimsOpenHandsGroup(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "agent-server")

	lmnr := rs.ScopeSpans().AppendEmpty()
	lmnr.Scope().SetName("lmnr.tracer")
	root := lmnr.Spans().AppendEmpty()
	root.SetTraceID(pcommon.TraceID([16]byte{1}))
	root.SetSpanID(pcommon.SpanID([8]byte{2}))
	root.SetName("conversation")
	root.Attributes().PutStr("lmnr.association.properties.session_id", "conv-uuid")
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 23, 10, 0, 1, 0, time.UTC)))

	next := &routerSink{}
	router := newTracesRouter(next)
	require.NoError(t, router.ConsumeTraces(context.Background(), traces))

	require.Len(t, next.traces, 1)
	var names []string
	spans := next.traces[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for i := 0; i < spans.Len(); i++ {
		names = append(names, spans.At(i).Name())
	}
	require.Contains(t, names, "invoke_agent openhands")
	require.NotContains(t, names, "unrelated_span")
}
