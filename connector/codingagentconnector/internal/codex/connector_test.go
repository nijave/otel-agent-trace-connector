package codex

import (
	"context"
	"errors"
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
	err    error
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := ptrace.NewTraces()
	traces.CopyTo(copied)
	s.traces = append(s.traces, copied)
	return s.err
}
func (s *traceSink) all() []ptrace.Traces {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ptrace.Traces(nil), s.traces...)
}

func TestConnectorCorrelatesOutOfOrderBatchAndFinalizes(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	cfg.TurnTimeout = time.Second
	sink := &traceSink{}
	set := connector.Settings{ID: component.NewID(component.MustNewType("coding_agent")), TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	instance := newTestConnector(t, cfg, set, sink)
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
	cfg := NewDefaultConfig()
	sink := &traceSink{}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	base := time.Now()
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		testEvent("codex.user_prompt", base, nil), testEvent("codex.user_prompt", base.Add(time.Second), nil),
	)))
	require.Len(t, sink.all(), 1)
	require.Equal(t, "superseded", attrString(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0), "coding_agent.turn.finish_reason"))
}

func TestConnectorDeduplicatesRedeliveredEvents(t *testing.T) {
	cfg := NewDefaultConfig()
	sink := &traceSink{}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	require.NoError(t, instance.Start(context.Background(), nil))
	base := time.Now()
	batch := func() plog.Logs {
		return makeLogs(
			testEvent("codex.user_prompt", base, map[string]any{"model": "gpt-test"}),
			testEvent("codex.api_request", base.Add(time.Second), map[string]any{"duration_ms": int64(100)}),
			testEvent("codex.sse_event", base.Add(2*time.Second), map[string]any{"event.kind": "response.completed", "input_token_count": int64(5), "output_token_count": int64(2)}),
			testEvent("codex.tool_result", base.Add(3*time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1", "success": true}),
		)
	}
	// At-least-once OTLP delivery can resend the same batch before the turn is
	// finalized. Redelivery must not split the turn or double-count usage.
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch()))
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch()))
	require.Len(t, instance.turns, 1)
	require.NoError(t, instance.Shutdown(context.Background()))

	require.Len(t, sink.all(), 1)
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	root := findSpan(t, spans, "invoke_agent codex")
	require.Equal(t, int64(5), attrInt(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(2), attrInt(t, root, "gen_ai.usage.output_tokens"))
	chatCount, toolCount := 0, 0
	for i := 0; i < spans.Len(); i++ {
		switch attrString(t, spans.At(i), "gen_ai.operation.name") {
		case "chat":
			chatCount++
		case "execute_tool":
			toolCount++
		}
	}
	require.Equal(t, 1, chatCount)
	require.Equal(t, 1, toolCount)
}

func TestConnectorBoundsStateAndEvents(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.MaxActiveTurns = 1
	cfg.MaxEvents = 1
	sink := &traceSink{}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	first := testEvent("codex.user_prompt", time.Now(), nil)
	first.conversationID = "first"
	first.attrs["conversation.id"] = "first"
	second := testEvent("codex.user_prompt", time.Now().Add(time.Second), nil)
	second.conversationID = "second"
	second.attrs["conversation.id"] = "second"
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(first, second)))
	require.Len(t, sink.all(), 1)
	require.Equal(t, "evicted", attrString(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0), "coding_agent.turn.finish_reason"))
	require.Len(t, instance.turns, 1)
	for _, turn := range instance.turns {
		turn.add(testEvent("codex.api_request", time.Now().Add(2*time.Second), nil), time.Now(), cfg.MaxEvents)
		require.True(t, turn.truncated)
	}
}

func TestZeroReorderWindowFinalizesPromptly(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.ReorderWindow = 0
	cfg.TurnTimeout = time.Second
	sink := &traceSink{}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	require.NoError(t, instance.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })
	base := time.Now()
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		testEvent("codex.user_prompt", base, nil),
		testEvent("codex.sse_event", base.Add(time.Millisecond), map[string]any{"event.kind": "response.completed"}),
	)))
	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, 250*time.Millisecond, 5*time.Millisecond)
}

func TestCompletedTurnWinsOverTimeoutThreshold(t *testing.T) {
	cfg := NewDefaultConfig()
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, &traceSink{})
	now := time.Now()
	key := turnKey{provider: "codex", conversationID: "conversation-1"}
	instance.turns[key] = &turnState{key: key, completeSeen: true, lastSeen: now.Add(-2 * cfg.TurnTimeout)}
	turns, reasons := instance.collectReady(now)
	require.Len(t, turns, 1)
	require.Equal(t, []string{"completed"}, reasons)
}

func TestToolAfterCompletionRequiresAnotherCompletion(t *testing.T) {
	cfg := NewDefaultConfig()
	now := time.Now()
	turn := &turnState{first: now, last: now}
	turn.add(testEvent("codex.sse_event", now, map[string]any{"event.kind": "response.completed"}), now, cfg.MaxEvents)
	require.True(t, turn.completeSeen)
	turn.add(testEvent("codex.tool_result", now.Add(time.Second), map[string]any{"tool_name": "shell"}), now, cfg.MaxEvents)
	require.False(t, turn.completeSeen)
	turn.add(testEvent("codex.sse_event", now.Add(2*time.Second), map[string]any{"event.kind": "response.completed"}), now, cfg.MaxEvents)
	require.True(t, turn.completeSeen)
}

func TestShutdownFlushesIncompleteTurn(t *testing.T) {
	cfg := NewDefaultConfig()
	sink := &traceSink{}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	require.NoError(t, instance.Start(context.Background(), nil))
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(testEvent("codex.user_prompt", time.Now(), nil))))
	require.NoError(t, instance.Shutdown(context.Background()))
	require.Len(t, sink.all(), 1)
	require.Equal(t, "shutdown", attrString(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0), "coding_agent.turn.finish_reason"))
}

func TestShutdownWithoutStartDoesNotBlock(t *testing.T) {
	cfg := NewDefaultConfig()
	sink := &traceSink{}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(testEvent("codex.user_prompt", time.Now(), nil))))
	// Shutdown without a prior Start must not wait on the sweep loop's done
	// channel, which never closes because the loop never ran.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	require.NoError(t, instance.Shutdown(ctx))
	require.Len(t, sink.all(), 1)
}

func TestShutdownDrainContinuesAfterDownstreamError(t *testing.T) {
	cfg := NewDefaultConfig()
	sink := &traceSink{err: errors.New("downstream unavailable")}
	instance := newTestConnector(t, cfg, connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}, sink)
	require.NoError(t, instance.Start(context.Background(), nil))
	for i, id := range []string{"first", "second", "third"} {
		event := testEvent("codex.user_prompt", time.Now().Add(time.Duration(i)*time.Millisecond), nil)
		event.conversationID = id
		event.attrs["conversation.id"] = id
		require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(event)))
	}
	// A single downstream error must not abandon the remaining turns: every
	// active turn is still emitted and the failure is surfaced.
	err := instance.Shutdown(context.Background())
	require.Error(t, err)
	require.Len(t, sink.all(), 3)
}

func TestEmittedScopeVersionUsesBuildInfo(t *testing.T) {
	set := connector.Settings{
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
		BuildInfo:         component.BuildInfo{Version: "1.2.3"},
	}
	sink := &traceSink{}
	instance := newTestConnector(t, NewDefaultConfig(), set, sink)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(testEvent("codex.user_prompt", time.Now(), nil))))
	require.NoError(t, instance.Shutdown(context.Background()))
	require.Len(t, sink.all(), 1)
	scope := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Scope()
	require.Equal(t, "1.2.3", scope.Version())
}

func newTestConnector(t *testing.T, cfg *Config, set connector.Settings, next consumer.Traces) *codingAgentConnector {
	t.Helper()
	instance, err := newConnector(cfg, set, next)
	require.NoError(t, err)
	return instance
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
