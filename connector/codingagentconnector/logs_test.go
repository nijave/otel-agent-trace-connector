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
)

type logsSink struct {
	traces []ptrace.Traces
}

func (*logsSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *logsSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.traces = append(s.traces, traces)
	return nil
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
