// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claudelogs

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

type traceSink struct {
	traces []ptrace.Traces
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.traces = append(s.traces, traces)
	return nil
}

func newEdge(t *testing.T, next consumer.Traces, chatFromLogs, captureIdentity bool) connector.Logs {
	t.Helper()
	set := connector.Settings{
		ID:                component.NewID(component.MustNewType("coding_agent")),
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
	}
	return New(chatFromLogs, captureIdentity, set, next)
}

// claudeRecord builds one claimed-shaped log record with synthetic values.
func claudeRecord(name string) plog.LogRecord {
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code." + name)
	record.Attributes().PutStr("event.name", name)
	record.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	record.SetSpanID(pcommon.SpanID([8]byte{21, 22, 23, 24, 25, 26, 27, 28}))
	record.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)))
	return record
}

// recordingEdge wraps the real edge with its own sink so tests can read the
// emitted traces.
type recordingEdge struct {
	connector.Logs
	sink *traceSink
}

// newRecordingEdge wraps the real edge, with chats from logs and identity
// captured: the settings most tests want.
func newRecordingEdge(t *testing.T) *recordingEdge {
	return newRecordingEdgeWith(t, true, true)
}

func newRecordingEdgeWith(t *testing.T, chatFromLogs, captureIdentity bool) *recordingEdge {
	t.Helper()
	sink := &traceSink{}
	return &recordingEdge{Logs: newEdge(t, sink, chatFromLogs, captureIdentity), sink: sink}
}

func flatten(sink *traceSink) ptrace.SpanSlice {
	all := ptrace.NewSpanSlice()
	for _, traces := range sink.traces {
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

func attr(t *testing.T, span ptrace.Span, key string) pcommon.Value {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "span %q missing attribute %s", span.Name(), key)
	return value
}

func TestSynthesizesChatSpanFromAPIRequest(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_01SYNTHETIC")
	record.Attributes().PutStr("model", "claude-sonnet-4-5")
	record.Attributes().PutInt("input_tokens", 15234)
	record.Attributes().PutInt("output_tokens", 2048)
	record.Attributes().PutInt("cache_read_tokens", 4096)
	record.Attributes().PutInt("cache_creation_tokens", 512)
	record.Attributes().PutDouble("cost_usd", 0.1525)
	record.Attributes().PutInt("ttft_ms", 812)
	record.Attributes().PutInt("duration_ms", 5310)
	record.Attributes().PutStr("effort", "high")
	record.Attributes().PutStr("session.id", "synthetic-session-01")
	record.Attributes().PutStr("agent_id", "agent-synthetic-sub")
	record.Attributes().PutStr("terminal.type", "xterm-256color")
	record.Attributes().PutStr("user.id", "user_synthetic01")
	record.Attributes().PutStr("user.email", "synthetic.user@example.com")
	record.Attributes().PutStr("organization.id", "org-synthetic-01")

	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	require.Equal(t, 1, spans.Len())
	span := spans.At(0)

	require.Equal(t, "chat claude-sonnet-4-5", span.Name())
	require.Equal(t, pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}), span.TraceID())
	require.Equal(t, pcommon.SpanID([8]byte{21, 22, 23, 24, 25, 26, 27, 28}), span.ParentSpanID(), "log SpanId is the parent")
	require.Equal(t, chatSpanID("req_01SYNTHETIC"), span.SpanID())
	require.Equal(t, ptrace.SpanKindClient, span.Kind())
	start := span.StartTimestamp().AsTime()
	end := span.EndTimestamp().AsTime()
	require.Equal(t, time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), end)
	require.Equal(t, 5310*time.Millisecond, end.Sub(start))
	require.Equal(t, ptrace.StatusCodeUnset, span.Status().Code())

	require.Equal(t, "chat", attr(t, span, "gen_ai.operation.name").Str())
	require.Equal(t, "anthropic", attr(t, span, "gen_ai.provider.name").Str())
	require.Equal(t, "native", attr(t, span, "coding_agent.source").Str())
	require.Equal(t, "claude_code.api_request", attr(t, span, "coding_agent.source.event").Str())
	require.Equal(t, "claude_code", attr(t, span, "coding_agent.client.name").Str())
	require.Equal(t, "2.1.319", attr(t, span, "coding_agent.client.version").Str())
	require.Equal(t, "claude-sonnet-4-5", attr(t, span, "gen_ai.request.model").Str())
	require.Equal(t, "req_01SYNTHETIC", attr(t, span, "gen_ai.response.id").Str())
	require.Equal(t, int64(15234), attr(t, span, "gen_ai.usage.input_tokens").Int())
	require.Equal(t, int64(2048), attr(t, span, "gen_ai.usage.output_tokens").Int())
	require.Equal(t, int64(4096), attr(t, span, "gen_ai.usage.cache_read.input_tokens").Int())
	require.Equal(t, int64(512), attr(t, span, "gen_ai.usage.cache_write.input_tokens").Int())
	require.Equal(t, 0.812, attr(t, span, "gen_ai.response.time_to_first_chunk").Double())
	require.Equal(t, "high", attr(t, span, "gen_ai.request.reasoning.level").Str())
	require.Equal(t, 0.1525, attr(t, span, "coding_agent.usage.cost").Double())
	require.Equal(t, "synthetic-session-01", attr(t, span, "gen_ai.conversation.id").Str())
	require.Equal(t, "agent-synthetic-sub", attr(t, span, "gen_ai.agent.id").Str())
	require.Equal(t, "xterm-256color", attr(t, span, "coding_agent.terminal.type").Str())
	require.Equal(t, "user_synthetic01", attr(t, span, "coding_agent.user.id").Str())
	require.Equal(t, "synthetic.user@example.com", attr(t, span, "coding_agent.user.email").Str())
	require.Equal(t, "org-synthetic-01", attr(t, span, "coding_agent.team.id").Str())

	// Every emitted key is inside the vocabulary: no vendor leftovers such
	// as event.name, speed, or query_source survive.
	span.Attributes().Range(func(key string, _ pcommon.Value) bool {
		require.True(t, canonical.IsCanonicalAttribute(key), "forbidden attribute %s", key)
		return true
	})
	require.Equal(t, 0, span.Events().Len())
}

func batch(t *testing.T, scopeName string, records ...plog.LogRecord) plog.Logs {
	t.Helper()
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "claude-code")
	rl.Resource().Attributes().PutStr("service.version", "2.1.319")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName(scopeName)
	for _, record := range records {
		record.CopyTo(sl.LogRecords().AppendEmpty())
	}
	return logs
}

func TestChatSpanWithoutModelKeepsBareName(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_02SYNTHETIC")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	require.Equal(t, 1, spans.Len())
	require.Equal(t, "chat", spans.At(0).Name())
	_, ok := spans.At(0).Attributes().Get("gen_ai.request.model")
	require.False(t, ok)
}

func TestAPIErrorSetsErrorStatus(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_error")
	record.Attributes().PutStr("request_id", "req_03SYNTHETIC")
	record.Attributes().PutStr("model", "claude-sonnet-4-5")
	record.Attributes().PutStr("error", "rate limit exceeded")
	record.Attributes().PutStr("status_code", "429")
	record.Attributes().PutInt("duration_ms", 240)

	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	require.Equal(t, 1, spans.Len())
	span := spans.At(0)
	require.Equal(t, ptrace.StatusCodeError, span.Status().Code())
	require.Equal(t, "rate limit exceeded", span.Status().Message())
	// api_error carries no tokens, cost, or ttft_ms: none of those keys map.
	for _, key := range []string{
		"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
		"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_write.input_tokens",
		"coding_agent.usage.cost", "gen_ai.response.time_to_first_chunk",
	} {
		_, ok := span.Attributes().Get(key)
		require.False(t, ok, "%s must be absent on an api_error chat span", key)
	}
}

// TestAttemptMapsOntoChatSpan pins the retry-attempt mapping on the logs
// edge: api_error records carry `attempt` today, and a record that carries it
// maps onto coding_agent.request.attempt regardless of its event type —
// Claude Code reports a 1-based attempt number, so a failed attempt maps
// alongside a successful one. Int, double, and numeric-string values parse;
// an unparsable one stays absent.
func TestAttemptMapsOntoChatSpan(t *testing.T) {
	edge := newRecordingEdge(t)
	errorRecord := claudeRecord("api_error")
	errorRecord.Attributes().PutStr("request_id", "req_03SYNTHETIC")
	errorRecord.Attributes().PutStr("error", "rate limit exceeded")
	errorRecord.Attributes().PutInt("attempt", 2)
	requestRecord := claudeRecord("api_request")
	requestRecord.Attributes().PutStr("request_id", "req_07SYNTHETIC")
	requestRecord.Attributes().PutDouble("attempt", 1)
	stringRecord := claudeRecord("api_error")
	stringRecord.Attributes().PutStr("request_id", "req_08SYNTHETIC")
	stringRecord.Attributes().PutStr("attempt", "3")
	unparsableRecord := claudeRecord("api_error")
	unparsableRecord.Attributes().PutStr("request_id", "req_09SYNTHETIC")
	unparsableRecord.Attributes().PutStr("attempt", "not-a-number")

	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, errorRecord, requestRecord, stringRecord, unparsableRecord)))
	spans := flatten(edge.sink)
	require.Equal(t, 4, spans.Len())
	bySpanID := map[string]ptrace.Span{}
	for i := 0; i < spans.Len(); i++ {
		bySpanID[attr(t, spans.At(i), "gen_ai.response.id").Str()] = spans.At(i)
	}
	// api_error record: the failed attempt maps too.
	require.Equal(t, int64(2), attr(t, bySpanID["req_03SYNTHETIC"], "coding_agent.request.attempt").Int())
	// api_request carries no attempt on the wire today; if a record does, it
	// maps the same way.
	require.Equal(t, int64(1), attr(t, bySpanID["req_07SYNTHETIC"], "coding_agent.request.attempt").Int())
	// Numeric strings parse.
	require.Equal(t, int64(3), attr(t, bySpanID["req_08SYNTHETIC"], "coding_agent.request.attempt").Int())
	// Unparsable values stay absent.
	_, ok := bySpanID["req_09SYNTHETIC"].Attributes().Get("coding_agent.request.attempt")
	require.False(t, ok)
}

func TestZeroCostMapsAsZero(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_04SYNTHETIC")
	record.Attributes().PutDouble("cost_usd", 0)
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	require.Equal(t, 0.0, attr(t, spans.At(0), "coding_agent.usage.cost").Double())
}

func TestUnparsableCostStaysAbsent(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_05SYNTHETIC")
	record.Attributes().PutStr("cost_usd", "not-a-number")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	_, ok := spans.At(0).Attributes().Get("coding_agent.usage.cost")
	require.False(t, ok)
}

func TestStringCountersParse(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_06SYNTHETIC")
	record.Attributes().PutStr("input_tokens", "15234")
	record.Attributes().PutStr("output_tokens", "2048")
	record.Attributes().PutStr("cache_read_tokens", "0")
	record.Attributes().PutStr("cache_creation_tokens", "512")
	record.Attributes().PutStr("ttft_ms", "250")
	record.Attributes().PutStr("cost_usd", "0.5")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	attrs := spans.At(0).Attributes()
	require.Equal(t, int64(15234), attrsGet(t, attrs, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(2048), attrsGet(t, attrs, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(0), attrsGet(t, attrs, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(512), attrsGet(t, attrs, "gen_ai.usage.cache_write.input_tokens"))
	require.Equal(t, 0.25, attr(t, spans.At(0), "gen_ai.response.time_to_first_chunk").Double())
	require.Equal(t, 0.5, attr(t, spans.At(0), "coding_agent.usage.cost").Double())
}

func attrsGet(t *testing.T, attrs pcommon.Map, key string) int64 {
	t.Helper()
	value, ok := attrs.Get(key)
	require.True(t, ok)
	return value.Int()
}

func TestMissingRequestIDDrops(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	require.Empty(t, edge.sink.traces)
}

func TestMissingTraceContextDrops(t *testing.T) {
	edge := newRecordingEdge(t)
	noTrace := claudeRecord("api_request")
	noTrace.Attributes().PutStr("request_id", "req_07SYNTHETIC")
	noTrace.SetTraceID(pcommon.TraceID{})
	noSpan := claudeRecord("api_request")
	noSpan.Attributes().PutStr("request_id", "req_08SYNTHETIC")
	noSpan.SetSpanID(pcommon.SpanID{})
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, noTrace, noSpan)))
	require.Empty(t, edge.sink.traces, "records without trace context must drop")
}

// TestUnusableRecordLeavesNoEmptyGroup pins the no-empty-groups contract: a
// claimed record the edge rejects (zero trace id) in an earlier resource
// group must not leave an empty resource/scope group behind when a later
// group holds a usable record.
func TestUnusableRecordLeavesNoEmptyGroup(t *testing.T) {
	edge := newRecordingEdge(t)
	unusable := claudeRecord("api_request")
	unusable.Attributes().PutStr("request_id", "req_15SYNTHETIC")
	unusable.SetTraceID(pcommon.TraceID{})
	usable := claudeRecord("api_request")
	usable.Attributes().PutStr("request_id", "req_16SYNTHETIC")

	logs := plog.NewLogs()
	first := logs.ResourceLogs().AppendEmpty()
	first.Resource().Attributes().PutStr("service.name", "claude-code")
	firstScope := first.ScopeLogs().AppendEmpty()
	firstScope.Scope().SetName(ScopeName)
	unusable.CopyTo(firstScope.LogRecords().AppendEmpty())
	second := logs.ResourceLogs().AppendEmpty()
	second.Resource().Attributes().PutStr("service.name", "claude-code")
	secondScope := second.ScopeLogs().AppendEmpty()
	secondScope.Scope().SetName(ScopeName)
	usable.CopyTo(secondScope.LogRecords().AppendEmpty())

	require.NoError(t, edge.ConsumeLogs(context.Background(), logs))
	require.Len(t, edge.sink.traces, 1)
	out := edge.sink.traces[0]
	require.Equal(t, 1, out.ResourceSpans().Len(),
		"the rejected record's group must not survive as an empty group")
	require.Equal(t, 1, out.ResourceSpans().At(0).ScopeSpans().Len())
	require.Equal(t, 1, out.ResourceSpans().At(0).ScopeSpans().At(0).Spans().Len())
	require.Equal(t, "req_16SYNTHETIC",
		out.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().AsRaw()["gen_ai.response.id"])
}

func TestZeroRecordTimestampFallsBackToEventTimestamp(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.SetTimestamp(0)
	record.Attributes().PutStr("request_id", "req_09SYNTHETIC")
	record.Attributes().PutStr("event.timestamp", "2026-11-07T12:00:30.25Z")
	record.Attributes().PutInt("duration_ms", 5000)
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	span := spans.At(0)
	require.Equal(t, time.Date(2026, 11, 7, 12, 0, 30, int(250*time.Millisecond), time.UTC), span.EndTimestamp().AsTime())
	require.Equal(t, time.Date(2026, 11, 7, 12, 0, 25, int(250*time.Millisecond), time.UTC), span.StartTimestamp().AsTime())
}

func TestUnusableTimestampDrops(t *testing.T) {
	edge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.SetTimestamp(0)
	record.Attributes().PutStr("request_id", "req_10SYNTHETIC")
	record.Attributes().PutStr("event.timestamp", "last tuesday")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	require.Empty(t, edge.sink.traces)
}

func TestForeignRecordsIgnored(t *testing.T) {
	edge := newRecordingEdge(t)
	otherEvent := claudeRecord("user_prompt")
	otherScope := claudeRecord("api_request")
	otherScope.Attributes().PutStr("request_id", "req_11SYNTHETIC")
	noEventName := plog.NewLogRecord()
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, otherEvent, noEventName)))
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, "other.scope", otherScope)))
	require.Empty(t, edge.sink.traces)
}

func TestCaptureIdentityOff(t *testing.T) {
	edge := newRecordingEdgeWith(t, true, false)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_12SYNTHETIC")
	record.Attributes().PutStr("user.id", "user_synthetic01")
	record.Attributes().PutStr("user.email", "synthetic.user@example.com")
	record.Attributes().PutStr("organization.id", "org-synthetic-01")
	record.Attributes().PutStr("terminal.type", "xterm-256color")
	record.Attributes().PutStr("agent_id", "agent-synthetic-sub")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	spans := flatten(edge.sink)
	attrs := spans.At(0).Attributes()
	for _, key := range []string{"coding_agent.user.id", "coding_agent.user.email", "coding_agent.team.id"} {
		_, ok := attrs.Get(key)
		require.False(t, ok, "%s must be gated behind capture_identity", key)
	}
	// Neither of these is identity, so they ship regardless.
	require.Equal(t, "xterm-256color", attr(t, spans.At(0), "coding_agent.terminal.type").Str())
	require.Equal(t, "agent-synthetic-sub", attr(t, spans.At(0), "gen_ai.agent.id").Str())
}

func TestChatSourceTracesEmitsNothing(t *testing.T) {
	edge := newRecordingEdgeWith(t, false, true)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_13SYNTHETIC")
	require.NoError(t, edge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	require.Empty(t, edge.sink.traces)
}

func TestChatSpanIDIsFirstEightSHA256Bytes(t *testing.T) {
	sum := sha256.Sum256([]byte("req_01SYNTHETIC"))
	var want pcommon.SpanID
	copy(want[:], sum[:8])
	require.Equal(t, want, chatSpanID("req_01SYNTHETIC"))
}

// TestLogChatSpanJoinsNativeInteractionParent pins the cross-edge contract
// the design's evidence section states: the log record's SpanId is the native
// interaction (or tool execution) span id, so the synthesized chat span's
// TestLogChatSpanJoinsNativeInteractionParent pins that the synthesized chat
// span hangs off the interaction span the log record names: the record carries
// the native interaction's span id as its own, and the Claude traces edge
// preserves that id verbatim (internal/claude owns that assertion), so both
// wires agree on the parent without the edges knowing about each other.
func TestLogChatSpanJoinsNativeInteractionParent(t *testing.T) {
	traceID := pcommon.TraceID([16]byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 2, 3, 4, 5, 6, 7, 8})
	interactionID := pcommon.SpanID([8]byte{17, 18, 19, 20, 21, 22, 23, 24})

	logsEdge := newRecordingEdge(t)
	record := claudeRecord("api_request")
	record.Attributes().PutStr("request_id", "req_14SYNTHETIC")
	record.Attributes().PutStr("model", "claude-sonnet-4-5")
	record.SetTraceID(traceID)
	record.SetSpanID(interactionID)
	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), batch(t, ScopeName, record)))
	chats := flatten(logsEdge.sink)
	require.Equal(t, 1, chats.Len())
	require.Equal(t, traceID, chats.At(0).TraceID())
	require.Equal(t, interactionID, chats.At(0).ParentSpanID(),
		"the synthesized chat span's parent must be the native interaction span id the record carries")
}

func TestStartAndShutdownAreNoOps(t *testing.T) {
	edge := newRecordingEdge(t)
	require.NoError(t, edge.Start(context.Background(), nil))
	require.NoError(t, edge.Shutdown(context.Background()))
}

func TestDoesNotMutateInput(t *testing.T) {
	edge := newRecordingEdge(t)
	logs := batch(t, ScopeName, claudeRecord("api_request"))
	require.NoError(t, edge.ConsumeLogs(context.Background(), logs))
	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	require.Equal(t, "api_request", record.Attributes().AsRaw()["event.name"],
		"the input record must pass through untouched")
}
