// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"
	"strings"
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

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

type logsSink struct {
	traces []ptrace.Traces
}

func (*logsSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *logsSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.traces = append(s.traces, traces)
	return nil
}

// TestLogsRouterSynthesizesClaudeChatsWhenChatSourceLogs pins the
// claude_chat_source: logs path through the real router: api_request and
// api_error records become chat spans, other Claude events stay ignored.
func TestLogsRouterSynthesizesClaudeChatsWhenChatSourceLogs(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.ClaudeChatSource = codex.ClaudeChatLogs
	sink := &logsSink{}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(cfg, set, sink)
	require.NoError(t, err)
	require.NoError(t, logsEdge.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, logsEdge.Shutdown(context.Background())) })

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "claude-code")
	claudeScope := rl.ScopeLogs().AppendEmpty()
	claudeScope.Scope().SetName("com.anthropic.claude_code.events")
	request := claudeScope.LogRecords().AppendEmpty()
	request.SetTraceID(pcommon.TraceID([16]byte{1}))
	request.SetSpanID(pcommon.SpanID([8]byte{2}))
	request.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)))
	request.Body().SetStr("claude_code.api_request")
	request.Attributes().PutStr("event.name", "api_request")
	request.Attributes().PutStr("request_id", "req-router-synthetic")
	request.Attributes().PutStr("model", "claude-sonnet-4-5")
	request.Attributes().PutDouble("cost_usd", 0.25)
	errorRecord := claudeScope.LogRecords().AppendEmpty()
	errorRecord.SetTraceID(pcommon.TraceID([16]byte{3}))
	errorRecord.SetSpanID(pcommon.SpanID([8]byte{4}))
	errorRecord.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 11, 7, 12, 0, 1, 0, time.UTC)))
	errorRecord.Body().SetStr("claude_code.api_error")
	errorRecord.Attributes().PutStr("event.name", "api_error")
	errorRecord.Attributes().PutStr("request_id", "req-router-error")
	errorRecord.Attributes().PutStr("error", "overloaded")
	prompt := claudeScope.LogRecords().AppendEmpty()
	prompt.Body().SetStr("claude_code.user_prompt")
	prompt.Attributes().PutStr("event.name", "user_prompt")

	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), logs))
	var names []string
	statuses := map[string]ptrace.StatusCode{}
	for _, traces := range sink.traces {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			require.Equal(t, "claude-code", rs.Resource().Attributes().AsRaw()["service.name"],
				"the canonical resource filter keeps service.name")
			_, hasRawSession := rs.Resource().Attributes().Get("session.id")
			require.False(t, hasRawSession, "non-canonical resource keys must drop")
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					names = append(names, spans.At(k).Name())
					statuses[spans.At(k).Name()] = spans.At(k).Status().Code()
				}
			}
		}
	}
	require.ElementsMatch(t, []string{"chat claude-sonnet-4-5", "chat"}, names,
		"the api_error chat span has no model, so its name stays bare")
	require.Equal(t, ptrace.StatusCodeUnset, statuses["chat claude-sonnet-4-5"])
}

// TestLogsRouterSkipsClaudeRecordsWhenChatSourceTraces pins the default:
// with claude_chat_source: traces the claudelogs edge emits nothing and the
// native traces edge owns chat spans.
func TestLogsRouterSkipsClaudeRecordsWhenChatSourceTraces(t *testing.T) {
	sink := &logsSink{}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, sink)
	require.NoError(t, err)

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	claudeScope := rl.ScopeLogs().AppendEmpty()
	claudeScope.Scope().SetName("com.anthropic.claude_code.events")
	request := claudeScope.LogRecords().AppendEmpty()
	request.SetTraceID(pcommon.TraceID([16]byte{1}))
	request.SetSpanID(pcommon.SpanID([8]byte{2}))
	request.Body().SetStr("claude_code.api_request")
	request.Attributes().PutStr("event.name", "api_request")
	request.Attributes().PutStr("request_id", "req-router-synthetic")

	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), logs))
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Empty(t, sink.traces, "claude_chat_source: traces: the logs edge must emit nothing")
}

func mixedLogs(t *testing.T) plog.Logs {
	t.Helper()
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()

	codexScope := rl.ScopeLogs().AppendEmpty()
	codexScope.Scope().SetName("codex_cli_rs")
	codexRecord := codexScope.LogRecords().AppendEmpty()
	codexRecord.Attributes().PutStr("event.name", "codex.user_prompt")
	codexRecord.Attributes().PutStr("conversation.id", "codex-conv-1")
	codexRecord.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)))

	cursorScope := rl.ScopeLogs().AppendEmpty()
	cursorScope.Scope().SetName("cursor.telemetry")
	cursorRecord := cursorScope.LogRecords().AppendEmpty()
	cursorRecord.Body().SetStr("api_request")
	cursorRecord.Attributes().PutStr("cursor.event.id", "ev-1")
	cursorRecord.Attributes().PutStr("cursor.conversation.id", "cursor-conv-1")
	cursorRecord.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 10, 0, 1, 0, time.UTC)))

	foreignScope := rl.ScopeLogs().AppendEmpty()
	foreignScope.Scope().SetName("unrelated")
	foreignRecord := foreignScope.LogRecords().AppendEmpty()
	foreignRecord.Body().SetStr("something_else")
	return logs
}

func TestLogsRouterClaimsDisjointSources(t *testing.T) {
	sink := &logsSink{}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, sink)
	require.NoError(t, err)
	require.NoError(t, logsEdge.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, logsEdge.Shutdown(context.Background())) })

	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), mixedLogs(t)))
	// Shutdown drains both open states; both edges emitted exactly one trace
	// and the foreign record produced none. The cursor trace carries chat
	// children beside its root, so match on root names rather than span counts.
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Len(t, sink.traces, 2)
	var rootNames []string
	for _, traces := range sink.traces {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			ss := traces.ResourceSpans().At(i).ScopeSpans()
			for j := 0; j < ss.Len(); j++ {
				spans := ss.At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					if name := spans.At(k).Name(); strings.HasPrefix(name, "invoke_agent") {
						rootNames = append(rootNames, name)
					}
				}
			}
		}
	}
	require.ElementsMatch(t, []string{"invoke_agent codex", "invoke_agent cursor"}, rootNames)
}

func TestLogsRouterShutdownSweepsBothEdges(t *testing.T) {
	// A router that embeds no-op StartFunc/ShutdownFunc (like tracesRouter)
	// would silently drop both edges' sweep loops and drain; this test pins
	// the fan-out by requiring shutdown-flushed traces from both sources.
	sink := &logsSink{}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, sink)
	require.NoError(t, err)
	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), mixedLogs(t)))
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Len(t, sink.traces, 2)
}

// TestLogsRouterOmitsCodexEdgeWhenTracesOnly pins the per-harness signals
// switch: with `codex: [traces]` the Codex logs edge is not constructed, so
// Codex records fall through unclaimed while Cursor — a single-implementation
// edge — still runs. The switch never gates the whole router.
func TestLogsRouterOmitsCodexEdgeWhenTracesOnly(t *testing.T) {
	sink := &logsSink{}
	cfg := createDefaultConfig()
	cfg.Signals = map[string][]string{codex.HarnessCodex: {codex.SignalTraces}}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(cfg, set, sink)
	require.NoError(t, err)
	require.Len(t, logsEdge.(*logsRouter).edges, 2, "cursor and claudelogs only")
	require.NoError(t, logsEdge.Start(context.Background(), nil))

	input := mixedLogs(t)
	before := input.LogRecordCount()
	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), input))
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Equal(t, before, input.LogRecordCount(), "input must pass through untouched")
	var rootNames []string
	for _, traces := range sink.traces {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			ss := traces.ResourceSpans().At(i).ScopeSpans()
			for j := 0; j < ss.Len(); j++ {
				spans := ss.At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					if name := spans.At(k).Name(); strings.HasPrefix(name, "invoke_agent") {
						rootNames = append(rootNames, name)
					}
				}
			}
		}
	}
	require.ElementsMatch(t, []string{"invoke_agent cursor"}, rootNames,
		"codex logs disabled: the codex record drops but cursor still normalizes")
}

// TestLogsRouterBuildsExpectedCodexEdgeSets pins the edge set per signals
// value: the default (`codex: [logs]`) and `codex: [logs, traces]` both build
// the three-edge logs router with the Codex edge claiming its record; only
// `codex: [traces]` leaves it out (covered by the test above).
func TestLogsRouterBuildsExpectedCodexEdgeSets(t *testing.T) {
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	for _, tc := range []struct {
		name    string
		signals map[string][]string
		wantLen int
	}{
		{name: "default codex: [logs]", signals: nil, wantLen: 3},
		{name: "codex: [logs, traces]", signals: map[string][]string{codex.HarnessCodex: {codex.SignalLogs, codex.SignalTraces}}, wantLen: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := createDefaultConfig()
			if tc.signals != nil {
				cfg.Signals = tc.signals
			}
			sink := &logsSink{}
			logsEdge, err := newLogsRouter(cfg, set, sink)
			require.NoError(t, err)
			require.Len(t, logsEdge.(*logsRouter).edges, tc.wantLen)
			require.NoError(t, logsEdge.Start(context.Background(), nil))
			require.NoError(t, logsEdge.ConsumeLogs(context.Background(), mixedLogs(t)))
			require.NoError(t, logsEdge.Shutdown(context.Background()))
			var rootNames []string
			for _, traces := range sink.traces {
				for i := 0; i < traces.ResourceSpans().Len(); i++ {
					ss := traces.ResourceSpans().At(i).ScopeSpans()
					for j := 0; j < ss.Len(); j++ {
						spans := ss.At(j).Spans()
						for k := 0; k < spans.Len(); k++ {
							if name := spans.At(k).Name(); strings.HasPrefix(name, "invoke_agent") {
								rootNames = append(rootNames, name)
							}
						}
					}
				}
			}
			require.ElementsMatch(t, []string{"invoke_agent codex", "invoke_agent cursor"}, rootNames)
		})
	}
}
