package codex

import (
	"bytes"
	"context"
	"errors"
	"os"
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
	key := "conversation-1"
	instance.turns[key] = &turnState{conversationID: key, completeSeen: true, lastSeen: now.Add(-2 * cfg.TurnTimeout)}
	finalized := instance.collectReady(now)
	require.Len(t, finalized, 1)
	require.Equal(t, "completed", finalized[0].reason)
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

// TestConnectorAgainstRealCodexCapture pins the connector to real Codex 0.144.1
// telemetry captured by the e2e harness (GLM-4.7 via the responses-proxy). It
// guards against Codex log-schema drift and, specifically, the timing-only
// duplicate response.completed that must not become a usage-less chat span. The
// same shape is emitted by real OpenAI Codex, so this also covers that path.
func TestConnectorAgainstRealCodexCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-native-logs.json")
	require.NoError(t, err)

	cfg := NewDefaultConfig()
	// The capture is fed as four separate batches, and the turn looks finalizable
	// after the second one. The window has to outlast the gaps between those calls or
	// a scheduling stall on a loaded runner splits the capture into two turns, so it
	// is generous rather than as short as the other tests here can afford.
	cfg.ReorderWindow = 250 * time.Millisecond
	cfg.TurnTimeout = time.Second
	sink := &traceSink{}
	set := connector.Settings{ID: component.NewID(component.MustNewType("coding_agent")), TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	instance := newTestConnector(t, cfg, set, sink)
	require.NoError(t, instance.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	unmarshaler := &plog.JSONUnmarshaler{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		logs, err := unmarshaler.UnmarshalLogs(line)
		require.NoError(t, err)
		require.NoError(t, instance.ConsumeLogs(context.Background(), logs))
	}
	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, 2*time.Second, 5*time.Millisecond)

	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	root := findSpan(t, spans, "invoke_agent codex")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID())
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.True(t, attrBool(t, root, "coding_agent.turn.complete"), "real capture must finalize as a completed turn")
	require.NotEmpty(t, attrString(t, root, "gen_ai.conversation.id"))
	require.Greater(t, attrInt(t, root, "gen_ai.usage.input_tokens"), int64(0))
	// The capture was recorded through the responses-proxy, so it pins the provider
	// label Codex reports for a custom provider -- and that gen_ai.provider.name is
	// left describing the wire protocol rather than being overwritten with it.
	require.Equal(t, "z.ai via responses-proxy", attrString(t, root, "coding_agent.model_provider"))
	require.Equal(t, "openai", attrString(t, root, "gen_ai.provider.name"))

	// Every chat span must carry usage: the timing-only duplicate completions
	// (no token counts) must not produce usage-less chat spans.
	chatSpans := 0
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		if attrString(t, span, "gen_ai.operation.name") == "chat" {
			chatSpans++
			require.Greater(t, attrInt(t, span, "gen_ai.usage.input_tokens"), int64(0), "chat span %q lacks usage", span.Name())
		}
	}
	require.Positive(t, chatSpans)

	tool := findSpan(t, spans, "execute_tool exec_command")
	require.Equal(t, "exec_command", attrString(t, tool, "gen_ai.tool.name"))

	// Content Codex logs on the raw events must be stripped from every span.
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		for _, key := range []string{"prompt", "arguments", "output"} {
			_, ok := span.Attributes().Get(key)
			require.False(t, ok, "span %q leaked sensitive attribute %q", span.Name(), key)
		}
	}
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
