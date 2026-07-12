package codingagentconnector

import (
	"context"
	"sync"
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

func TestConnectorCorrelatesOutOfOrderBatchAndFinalizes(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	cfg.TurnTimeout = time.Second
	sink := &traceSink{}
	set := connector.Settings{ID: component.NewID(componentType), TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	instance, err := NewFactory().CreateLogsToTraces(context.Background(), set, cfg, sink)
	require.NoError(t, err)
	require.NoError(t, instance.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Now().Add(-time.Second)
	logs := makeLogs(
		testEvent("codex.sse_event", base.Add(2*time.Second), map[string]any{"event.kind": "response.completed", "input_token_count": int64(5)}),
		testEvent("codex.user_prompt", base, map[string]any{"model": "gpt-test"}),
		testEvent("codex.tool_result", base.Add(time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1", "success": true}),
	)
	require.NoError(t, instance.ConsumeLogs(context.Background(), logs))
	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 3, sink.all()[0].SpanCount())
}

func TestConnectorSplitsConsecutivePrompts(t *testing.T) {
	cfg := createDefaultConfig()
	sink := &traceSink{}
	instance := newConnector(cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	base := time.Now()
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		testEvent("codex.user_prompt", base, nil), testEvent("codex.user_prompt", base.Add(time.Second), nil),
	)))
	require.Len(t, sink.all(), 1)
	require.Equal(t, "superseded", attrString(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0), "coding_agent.turn.finish_reason"))
}

func TestConnectorBoundsStateAndEvents(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.MaxActiveTurns = 1
	cfg.MaxEvents = 1
	sink := &traceSink{}
	instance := newConnector(cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	first := testEvent("codex.user_prompt", time.Now(), nil)
	first.conversationID = "first"
	first.attrs["conversation.id"] = "first"
	second := testEvent("codex.user_prompt", time.Now().Add(time.Second), nil)
	second.conversationID = "second"
	second.attrs["conversation.id"] = "second"
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(first, second)))
	require.Len(t, sink.all(), 1)
	require.Len(t, instance.turns, 1)
	for _, turn := range instance.turns {
		turn.add(testEvent("codex.api_request", time.Now().Add(2*time.Second), nil), time.Now(), cfg.MaxEvents)
		require.True(t, turn.truncated)
	}
}

func TestShutdownFlushesIncompleteTurn(t *testing.T) {
	cfg := createDefaultConfig()
	sink := &traceSink{}
	instance := newConnector(cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	require.NoError(t, instance.Start(context.Background(), nil))
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(testEvent("codex.user_prompt", time.Now(), nil))))
	require.NoError(t, instance.Shutdown(context.Background()))
	require.Len(t, sink.all(), 1)
	require.Equal(t, "shutdown", attrString(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0), "coding_agent.turn.finish_reason"))
}

func makeLogs(events ...agentEvent) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "codex_cli_rs")
	records := rl.ScopeLogs().AppendEmpty().LogRecords()
	for _, event := range events {
		record := records.AppendEmpty()
		record.SetTimestamp(pcommon.NewTimestampFromTime(event.timestamp))
		requireNoError(record.Attributes().FromRaw(event.attrs))
	}
	return logs
}

func requireNoError(err error) {
	if err != nil {
		panic(err)
	}
}
