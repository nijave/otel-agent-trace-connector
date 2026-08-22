// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/metadatatest"
)

type testRecord struct {
	body  string
	ts    time.Time
	attrs map[string]any
}

const testConversation = "11111111-2222-3333-4444-555555555555"

func baseAttrs(eventID string) map[string]any {
	return map[string]any{
		"cursor.event.id":        eventID,
		"cursor.source_event.id": "src-" + eventID,
		"cursor.conversation.id": testConversation,
	}
}

func makeCursorLogs(records ...testRecord) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "cursor")
	rl.Resource().Attributes().PutStr("cursor.surface", "cli")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("cursor.telemetry")
	sl.Scope().SetVersion("0.1.0")
	for _, rec := range records {
		attrs := baseAttrs("ev-" + rec.body + "-" + rec.ts.Format(time.RFC3339Nano))
		for k, v := range rec.attrs {
			attrs[k] = v
		}
		record := sl.LogRecords().AppendEmpty()
		record.Body().SetStr(rec.body)
		record.SetTimestamp(pcommon.NewTimestampFromTime(rec.ts))
		requireNoError(record.Attributes().FromRaw(attrs))
	}
	return logs
}

func apiRequest(ts time.Time, eventID, model string, in, out int64) testRecord {
	attrs := map[string]any{
		"cursor.usage_event.id":                    "ue-" + eventID,
		"cursor.api.request.input_tokens":          in,
		"cursor.api.request.output_tokens":         out,
		"cursor.api.request.cache_read_tokens":     int64(0),
		"cursor.api.request.cache_creation_tokens": int64(0),
	}
	if model != "" {
		attrs["cursor.model.name"] = model
	}
	return testRecord{body: BodyAPIRequest, ts: ts, attrs: attrs}
}

type traceSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = append(s.traces, traces)
	return nil
}

func (s *traceSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.traces)
}

func testSettings() connector.Settings {
	return connector.Settings{
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
	}
}

func TestConsumeLogsOpensOneBurstPerConversation(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "claude-4.5-sonnet", 10, 20),
		apiRequest(base.Add(2*time.Second), "b", "claude-4.5-sonnet", 5, 5),
	)))
	require.Len(t, instance.bursts, 1)
	require.Len(t, instance.bursts[testConversation].events, 2)
}

func TestDedupeDropsRedeliveredRecords(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	batch := makeCursorLogs(apiRequest(base, "a", "gpt-5.2", 10, 20))
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch))
	// At-least-once redelivery of the same batch must not double-count.
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch))
	require.Len(t, instance.bursts[testConversation].events, 1)
}

func TestQuietWindowClosesBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	sink := &traceSink{}
	connectorLogs, err := New(cfg, testSettings(), sink)
	require.NoError(t, err)
	require.NoError(t, connectorLogs.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, connectorLogs.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, connectorLogs.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "gpt-5.2", 10, 20),
	)))
	require.Eventually(t, func() bool { return sink.count() == 1 }, time.Second, 5*time.Millisecond)
}

func TestTimeoutClosesLongBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	cfg.TurnTimeout = time.Second
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	require.NoError(t, instance.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	// Direct state injection (the codex connector tests use the same
	// technique): a burst old enough to hit turn_timeout while lastSeen stays
	// too recent for the quiet window, proving timeout wins the ordering.
	now := time.Now()
	instance.bursts[testConversation] = &burstState{
		conversationID: testConversation,
		events:         []Event{{Body: BodyAPIRequest, EventID: "ev-x", ConversationID: testConversation, Timestamp: now.Add(-2 * cfg.TurnTimeout)}},
		seen:           map[string]struct{}{"ev-x": {}},
		first:          now.Add(-2 * cfg.TurnTimeout),
		last:           now.Add(-2 * cfg.TurnTimeout),
		lastSeen:       now.Add(-cfg.ReorderWindow),
	}
	finalized := instance.collectReady(now)
	require.Len(t, finalized, 1)
	require.Equal(t, "timeout", finalized[0].reason)
}

func TestQuietBeatsNothingWhenBurstIdle(t *testing.T) {
	// Only ReorderWindow shrinks: with the default 10m turn_timeout the
	// burst is far too young for timeout, so quiet is the only ready reason.
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	now := time.Now()
	instance.bursts[testConversation] = &burstState{
		conversationID: testConversation,
		events:         []Event{{Body: BodyAPIRequest, EventID: "ev-x", ConversationID: testConversation, Timestamp: now.Add(-time.Second)}},
		seen:           map[string]struct{}{"ev-x": {}},
		first:          now.Add(-time.Second),
		last:           now.Add(-time.Second),
		lastSeen:       now.Add(-time.Second),
	}
	finalized := instance.collectReady(now)
	require.Len(t, finalized, 1)
	require.Equal(t, "quiet", finalized[0].reason)
}

func TestResumedConversationOpensNewBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "gpt-5.2", 10, 20),
	)))
	// Simulate the quiet close having happened, then a much later record.
	require.NoError(t, instance.flushAll(context.Background(), "quiet"))
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base.Add(time.Hour), "b", "gpt-5.2", 1, 2),
	)))
	require.Len(t, instance.bursts, 1)
	require.Equal(t, base.Add(time.Hour), instance.bursts[testConversation].first)
}

func TestEvictionBoundsActiveBursts(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.MaxActiveTurns = 1
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	first := makeCursorLogs(apiRequest(base, "a", "m", 1, 1))
	first.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr("cursor.conversation.id", "conv-first")
	second := makeCursorLogs(apiRequest(base.Add(time.Second), "b", "m", 1, 1))
	second.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr("cursor.conversation.id", "conv-second")
	require.NoError(t, instance.ConsumeLogs(context.Background(), first))
	require.NoError(t, instance.ConsumeLogs(context.Background(), second))
	require.Len(t, instance.bursts, 1)
	require.Contains(t, instance.bursts, "conv-second")
}

func TestTruncationSetsMarker(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.MaxEvents = 1
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "m", 1, 1),
		apiRequest(base.Add(time.Second), "b", "m", 1, 1),
	)))
	require.True(t, instance.bursts[testConversation].truncated)
	require.Len(t, instance.bursts[testConversation].events, 1)
}

func TestIgnoresForeignAndScopelessRecords(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	foreign := rl.ScopeLogs().AppendEmpty()
	foreign.Scope().SetName("some.other.scope")
	record := foreign.LogRecords().AppendEmpty()
	record.Body().SetStr(BodyAPIRequest)
	record.Attributes().PutStr("cursor.conversation.id", testConversation)
	require.NoError(t, instance.ConsumeLogs(context.Background(), logs))
	require.Empty(t, instance.bursts)
}

func TestShutdownFlushesOpenBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	sink := &traceSink{}
	connectorLogs, err := New(cfg, testSettings(), sink)
	require.NoError(t, err)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, connectorLogs.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "m", 1, 1),
	)))
	require.NoError(t, connectorLogs.Shutdown(context.Background()))
	require.Equal(t, 1, sink.count())
}

type failingSink struct{}

func (*failingSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (*failingSink) ConsumeTraces(context.Context, ptrace.Traces) error {
	return errors.New("downstream unavailable")
}

func TestEmittedTurnMetricSkipsFailedDelivery(t *testing.T) {
	testTel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, testTel.Shutdown(context.Background())) })

	connectorLogs, err := New(codex.NewDefaultConfig(), metadatatest.NewSettings(testTel), &failingSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	// Start is never called, so Shutdown drains synchronously without a sweep loop.

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "m", 1, 1),
	)))
	require.Len(t, instance.bursts, 1)

	require.Error(t, instance.Shutdown(context.Background()))

	var rm sdkmetric.ResourceMetrics
	require.NoError(t, testTel.Reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			require.NotEqual(t, "otelcol_coding_agent_turns_emitted", m.Name,
				"a failed delivery must not count as an emitted turn")
		}
	}
}

func TestEmittedTurnMetricCountsSuccessfulDelivery(t *testing.T) {
	testTel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, testTel.Shutdown(context.Background())) })

	sink := &traceSink{}
	connectorLogs, err := New(codex.NewDefaultConfig(), metadatatest.NewSettings(testTel), sink)
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "m", 1, 1),
	)))
	require.NoError(t, instance.Shutdown(context.Background()))

	metadatatest.AssertEqualCodingAgentTurnsEmitted(t, testTel,
		[]sdkmetric.DataPoint[int64]{{
			Attributes: attribute.NewSet(attribute.String("finish_reason", "shutdown")),
			Value:      1,
		}},
		metricdatatest.IgnoreTimestamp())
}
