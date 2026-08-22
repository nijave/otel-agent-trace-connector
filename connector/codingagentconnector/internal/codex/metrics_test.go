package codex

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

func newMeteredConnector(t *testing.T, cfg *Config, next consumer.Traces) (*codingAgentConnector, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop(), MeterProvider: provider}}
	instance := newTestConnector(t, cfg, set, next)
	t.Cleanup(func() {
		require.NoError(t, instance.Shutdown(context.Background()))
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return instance, reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

func counterValue(t *testing.T, rm metricdata.ResourceMetrics, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	want := attribute.NewSet(attrs...)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %s is not an int64 sum", name)
			var total int64
			for _, dp := range sum.DataPoints {
				if len(attrs) == 0 || dp.Attributes.Equals(&want) {
					total += dp.Value
				}
			}
			return total
		}
	}
	t.Fatalf("counter %s not found", name)
	return 0
}

func gaugeValue(t *testing.T, rm metricdata.ResourceMetrics, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	want := attribute.NewSet(attrs...)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "metric %s is not an int64 gauge", name)
			require.NotEmpty(t, g.DataPoints)
			for _, dp := range g.DataPoints {
				if dp.Attributes.Equals(&want) {
					return dp.Value
				}
			}
			t.Fatalf("gauge %s has no datapoint for %v", name, want)
		}
	}
	t.Fatalf("gauge %s not found", name)
	return 0
}

func promptEvent(id string, ts time.Time) agentEvent {
	event := testEvent("codex.user_prompt", ts, nil)
	event.conversationID = id
	event.attrs[attrConversationID] = id
	return event
}

func TestTelemetryCountsEmittedTurnsByReason(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.MaxActiveTurns = 1
	instance, reader := newMeteredConnector(t, cfg, &traceSink{})
	// max_active_turns=1 forces the first conversation to be evicted when the
	// second arrives; Shutdown then finalizes the survivor as "shutdown".
	base := time.Now()
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		promptEvent("first", base),
		promptEvent("second", base.Add(time.Second)),
	)))
	require.NoError(t, instance.Shutdown(context.Background()))

	rm := collectMetrics(t, reader)
	require.Equal(t, int64(1), counterValue(t, rm, "otelcol_coding_agent_turns_emitted", attribute.String("finish_reason", "evicted")))
	require.Equal(t, int64(1), counterValue(t, rm, "otelcol_coding_agent_turns_emitted", attribute.String("finish_reason", "shutdown")))
}

func TestTelemetryCountsDroppedDuplicateEvents(t *testing.T) {
	instance, reader := newMeteredConnector(t, NewDefaultConfig(), &traceSink{})
	base := time.Now()
	batch := func() plog.Logs {
		return makeLogs(
			testEvent("codex.user_prompt", base, map[string]any{"model": "gpt-test"}),
			testEvent("codex.api_request", base.Add(time.Second), map[string]any{"duration_ms": int64(100)}),
		)
	}
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch()))
	// At-least-once redelivery of the same batch must be counted as dropped, not
	// re-correlated.
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch()))

	rm := collectMetrics(t, reader)
	require.Equal(t, int64(2), counterValue(t, rm, "otelcol_coding_agent_events_dropped"))
}

func TestTelemetryCountsTruncatedTurns(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.MaxEvents = 1
	instance, reader := newMeteredConnector(t, cfg, &traceSink{})
	base := time.Now()
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		testEvent("codex.user_prompt", base, nil),
		testEvent("codex.api_request", base.Add(time.Second), map[string]any{"duration_ms": int64(10)}),
	)))
	require.NoError(t, instance.Shutdown(context.Background()))

	rm := collectMetrics(t, reader)
	require.Equal(t, int64(1), counterValue(t, rm, "otelcol_coding_agent_turns_truncated"))
	require.Equal(t, int64(1), counterValue(t, rm, "otelcol_coding_agent_turns_emitted", attribute.String("finish_reason", "shutdown")))
}

func TestTelemetryReportsActiveTurns(t *testing.T) {
	instance, reader := newMeteredConnector(t, NewDefaultConfig(), &traceSink{})
	base := time.Now()
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		promptEvent("a", base),
		promptEvent("b", base),
	)))
	rm := collectMetrics(t, reader)
	require.Equal(t, int64(2), gaugeValue(t, rm, "otelcol_coding_agent_active_turns", attribute.String("provider", "codex")))
}
