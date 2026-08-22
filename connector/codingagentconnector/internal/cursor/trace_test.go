// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func burstForTest() *burstState {
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Body: BodyAPIRequest, EventID: "ev-1", ConversationID: testConversation, UsageEventID: "ue-1",
			Timestamp: base, Resource: testResourceRaw(),
			Attrs: map[string]any{
				"cursor.model.name":               "claude-4.5-sonnet",
				"cursor.api.request.input_tokens": int64(100), "cursor.api.request.output_tokens": int64(200),
				"cursor.api.request.cache_read_tokens": int64(50), "cursor.api.request.cache_creation_tokens": int64(10),
				"cursor.api.billable": true,
			}},
		{Body: BodyAPIRequest, EventID: "ev-2", ConversationID: testConversation, UsageEventID: "ue-2",
			Timestamp: base.Add(2 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{
				"cursor.api.request.input_tokens": int64(7), "cursor.api.request.output_tokens": int64(9),
				"cursor.api.request.cache_read_tokens": int64(0), "cursor.api.request.cache_creation_tokens": int64(0),
			}},
		{Body: BodyAPIError, EventID: "ev-3", ConversationID: testConversation, UsageEventID: "ue-2",
			Timestamp: base.Add(3 * time.Second), Resource: testResourceRaw(), Attrs: map[string]any{}},
		{Body: "api_correction_not_billed_errored", EventID: "ev-4", ConversationID: testConversation, UsageEventID: "ue-2",
			Timestamp: base.Add(4 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.api.correction.kind": "not_billed_errored"}},
		{Body: BodySkillActivated, EventID: "ev-5", ConversationID: testConversation,
			Timestamp: base.Add(5 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.skill.name": "code-review", "cursor.skill.trigger": "agent_read", "cursor.skill.source": "user"}},
	}
	return &burstState{
		conversationID: testConversation,
		events:         events,
		seen:           map[string]struct{}{},
		resource:       testResourceRaw(),
		first:          base, last: base.Add(5 * time.Second), lastSeen: base.Add(5 * time.Second),
	}
}

func testResourceRaw() map[string]any {
	return map[string]any{
		"service.name": "cursor", "service.version": "1.16.5",
		"cursor.team.id": int64(4242), "cursor.surface": "cli", "cursor.entrypoint": "cli", "cursor.user.id": int64(99),
	}
}

func mustBuildTrace(t *testing.T, burst *burstState, reason string) ptrace.Traces {
	t.Helper()
	traces, err := buildTrace(burst, reason, "0.1.0")
	require.NoError(t, err)
	return traces
}

func spansByName(traces ptrace.Traces) map[string][]ptrace.Span {
	out := map[string][]ptrace.Span{}
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		ss := traces.ResourceSpans().At(i).ScopeSpans()
		for j := 0; j < ss.Len(); j++ {
			spans := ss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				out[span.Name()] = append(out[span.Name()], span)
			}
		}
	}
	return out
}

func TestBuildTraceRoot(t *testing.T) {
	traces := mustBuildTrace(t, burstForTest(), "quiet")
	roots := spansByName(traces)["invoke_agent cursor"]
	require.Len(t, roots, 1)
	root := roots[0]
	require.Equal(t, "invoke_agent", stringAttrOn(t, root, "gen_ai.operation.name"))
	require.Equal(t, "cursor", stringAttrOn(t, root, "gen_ai.agent.name"))
	require.Equal(t, testConversation, stringAttrOn(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "cursor", stringAttrOn(t, root, "coding_agent.client.name"))
	require.Equal(t, "1.16.5", stringAttrOn(t, root, "coding_agent.client.version"))
	require.Equal(t, "normalized", stringAttrOn(t, root, "telemetry.source"))
	require.Equal(t, "quiet", stringAttrOn(t, root, "coding_agent.turn.finish_reason"))
	require.Equal(t, "cli", stringAttrOn(t, root, "coding_agent.cursor.surface"))
	require.Equal(t, "cli", stringAttrOn(t, root, "coding_agent.cursor.entrypoint"))
	require.Equal(t, int64(4242), intAttrOn(t, root, "coding_agent.cursor.team.id"))
	require.Equal(t, int64(99), intAttrOn(t, root, "coding_agent.cursor.user.id"))
	// The connector never claims completion and never guesses a provider.
	_, ok := root.Attributes().Get("coding_agent.turn.complete")
	require.False(t, ok)
	_, ok = root.Attributes().Get("gen_ai.provider.name")
	require.False(t, ok)
	// Usage rollup sums both api_request records.
	require.Equal(t, int64(107), intAttrOn(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(209), intAttrOn(t, root, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(50), intAttrOn(t, root, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(10), intAttrOn(t, root, "gen_ai.usage.cache_creation.input_tokens"))
	// Bounds cover first..last event timestamps.
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.Equal(t, base, root.StartTimestamp().AsTime())
	require.Equal(t, base.Add(5*time.Second), root.EndTimestamp().AsTime())
}

func TestBuildTraceChatSpans(t *testing.T) {
	traces := mustBuildTrace(t, burstForTest(), "quiet")
	chats := spansByName(traces)["chat claude-4.5-sonnet"]
	require.Len(t, chats, 1)
	chat := chats[0]
	require.Equal(t, "chat", stringAttrOn(t, chat, "gen_ai.operation.name"))
	require.Equal(t, "claude-4.5-sonnet", stringAttrOn(t, chat, "gen_ai.request.model"))
	require.Equal(t, BodyAPIRequest, stringAttrOn(t, chat, "coding_agent.source.event"))
	require.Equal(t, int64(100), intAttrOn(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(200), intAttrOn(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, true, boolAttrOn(t, chat, "coding_agent.cursor.billable"))
	// Point span: the wire reports tokens at request grain with no timing.
	require.Equal(t, chat.StartTimestamp(), chat.EndTimestamp())
	require.Equal(t, rootOf(t, traces).SpanID(), chat.ParentSpanID())
	require.Equal(t, rootOf(t, traces).TraceID(), chat.TraceID())
}

func TestBuildTraceModellessChatKeepsBareName(t *testing.T) {
	traces := mustBuildTrace(t, burstForTest(), "quiet")
	chats := spansByName(traces)["chat"]
	require.Len(t, chats, 1)
	_, ok := chats[0].Attributes().Get("gen_ai.request.model")
	require.False(t, ok)
}

func TestBuildTraceErrorJoinAndCorrectionJoin(t *testing.T) {
	traces := mustBuildTrace(t, burstForTest(), "quiet")
	// ev-2 (ue-2) has an api_error and a correction; both attach there.
	chat := spansByName(traces)["chat"][0]
	require.Equal(t, ptrace.StatusCodeError, chat.Status().Code())
	require.Equal(t, 1, chat.Events().Len())
	require.Equal(t, "api_correction_not_billed_errored", chat.Events().At(0).Name())
	require.Equal(t, "not_billed_errored", chat.Events().At(0).Attributes().AsRaw()["coding_agent.cursor.correction.kind"])
	require.Equal(t, "ue-2", chat.Events().At(0).Attributes().AsRaw()["coding_agent.cursor.usage_event_id"])
	// The unjoined api_error/correction never land on the root in this burst.
	root := rootOf(t, traces)
	for i := 0; i < root.Events().Len(); i++ {
		name := root.Events().At(i).Name()
		require.NotEqual(t, BodyAPIError, name)
		require.False(t, IsCorrectionBody(name))
	}
}

func TestBuildTraceRootEvents(t *testing.T) {
	traces := mustBuildTrace(t, burstForTest(), "quiet")
	root := rootOf(t, traces)
	require.Equal(t, 1, root.Events().Len())
	event := root.Events().At(0)
	require.Equal(t, BodySkillActivated, event.Name())
	require.Equal(t, "code-review", event.Attributes().AsRaw()["coding_agent.cursor.skill.name"])
	require.Equal(t, "agent_read", event.Attributes().AsRaw()["coding_agent.cursor.skill.trigger"])
	require.Equal(t, "user", event.Attributes().AsRaw()["coding_agent.cursor.skill.source"])
	require.Equal(t, "ev-5", event.Attributes().AsRaw()["coding_agent.cursor.event_id"])
}

func TestBuildTraceUnjoinedErrorAndCorrectionLandOnRoot(t *testing.T) {
	burst := burstForTest()
	// Drop the api_request carrying ue-2 so its error and correction have no
	// join target inside the burst.
	burst.events = append(burst.events[:1], burst.events[2:]...)
	traces := mustBuildTrace(t, burst, "quiet")
	root := rootOf(t, traces)
	var names []string
	for i := 0; i < root.Events().Len(); i++ {
		names = append(names, root.Events().At(i).Name())
	}
	require.Contains(t, names, BodyAPIError)
	require.Contains(t, names, "api_correction_not_billed_errored")
}

func TestBuildTraceTimeoutSetsErrorStatus(t *testing.T) {
	traces := mustBuildTrace(t, burstForTest(), "timeout")
	require.Equal(t, ptrace.StatusCodeError, rootOf(t, traces).Status().Code())
}

func TestBuildTraceReportsResourceCopyFailure(t *testing.T) {
	burst := burstForTest()
	// chan int cannot round-trip into pdata; FromRaw rejects it.
	burst.resource = map[string]any{"service.name": "cursor", "poison": make(chan int)}
	traces, err := buildTrace(burst, "quiet", "0.1.0")
	require.Error(t, err)
	require.NotNil(t, traces)
	require.Len(t, spansByName(traces)["invoke_agent cursor"], 1)
}

func TestBuildTraceDeterministicIDs(t *testing.T) {
	first := mustBuildTrace(t, burstForTest(), "quiet")
	second := mustBuildTrace(t, burstForTest(), "quiet")
	require.Equal(t, rootOf(t, first).TraceID(), rootOf(t, second).TraceID())
	require.Equal(t, rootOf(t, first).SpanID(), rootOf(t, second).SpanID())

	// A different first event id changes the trace id.
	burst := burstForTest()
	burst.events[0].EventID = "ev-other"
	third := mustBuildTrace(t, burst, "quiet")
	require.NotEqual(t, rootOf(t, first).TraceID(), rootOf(t, third).TraceID())
}

func TestBuildTraceCopiesNothingOutsideAllowlist(t *testing.T) {
	burst := burstForTest()
	burst.events = append(burst.events, Event{
		Body: "future_event_kind", EventID: "ev-9", ConversationID: testConversation,
		Timestamp: burst.last.Add(time.Second), Resource: testResourceRaw(),
		Attrs: map[string]any{
			"cursor.event.id": "ev-9", "cursor.conversation.id": testConversation,
			"cursor.some.new.field": "prompt-like content that must not propagate",
		},
	})
	traces := mustBuildTrace(t, burst, "quiet")
	spans := spansByName(traces)
	for _, group := range spans {
		for _, span := range group {
			_, ok := span.Attributes().Get("cursor.some.new.field")
			require.False(t, ok, "span %s leaked a non-allowlisted attribute", span.Name())
		}
	}
	root := rootOf(t, traces)
	var generic bool
	for i := 0; i < root.Events().Len(); i++ {
		raw := root.Events().At(i).Attributes().AsRaw()
		_, leaked := raw["cursor.some.new.field"]
		require.False(t, leaked)
		if root.Events().At(i).Name() == "future_event_kind" {
			generic = true
			require.Len(t, raw, 1) // only the event id
		}
	}
	require.True(t, generic)
}

func stringAttrOn(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Str()
}

func intAttrOn(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Int()
}

func boolAttrOn(t *testing.T, span ptrace.Span, key string) bool {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Bool()
}

func rootOf(t *testing.T, traces ptrace.Traces) ptrace.Span {
	t.Helper()
	roots := spansByName(traces)["invoke_agent cursor"]
	require.Len(t, roots, 1)
	return roots[0]
}
