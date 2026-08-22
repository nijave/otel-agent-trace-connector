// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

func loadFixtureLogs(t *testing.T) plog.Logs {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cursor-native-logs.json"))
	require.NoError(t, err)
	unmarshaler := &plog.JSONUnmarshaler{}
	logs, err := unmarshaler.UnmarshalLogs(raw)
	require.NoError(t, err)
	return logs
}

// replayFixture feeds the fixture through a live connector: burst A's records,
// a wall-clock gap past the quiet window, then burst B's records. The source
// timestamps are fixed, so emitted spans are byte-deterministic even though
// finalization rides the real clock.
func replayFixture(t *testing.T, shuffle bool) []ptrace.Traces {
	t.Helper()
	sink := &traceSink{}
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 20 * time.Millisecond
	// The timeout clock measures from the burst's first source timestamp, which
	// is fixed near 2026-08-21T10:00Z; a short turn_timeout would trip the
	// moment wall-clock time moves past it and flip closures to "timeout"
	// nondeterministically. Keep it far beyond any possible test run so quiet
	// and shutdown are the only reachable closers.
	cfg.TurnTimeout = 365 * 24 * time.Hour
	connectorLogs, err := New(cfg, testSettings(), sink)
	require.NoError(t, err)
	require.NoError(t, connectorLogs.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, connectorLogs.Shutdown(context.Background())) })

	// Split by source timestamp into burst A and burst B batches. Every record
	// keeps its original resource and instrumentation scope, so the fixture's
	// foreign-scope record exercises the claiming rejection end to end.
	all := loadFixtureLogs(t)
	first, second := plog.NewLogs(), plog.NewLogs()
	batches := []*plog.Logs{&first, &second}
	for _, dst := range batches {
		rl := dst.ResourceLogs().AppendEmpty()
		all.ResourceLogs().At(0).Resource().CopyTo(rl.Resource())
		for j := 0; j < all.ResourceLogs().At(0).ScopeLogs().Len(); j++ {
			sourceScope := all.ResourceLogs().At(0).ScopeLogs().At(j)
			scope := rl.ScopeLogs().AppendEmpty()
			scope.Scope().SetName(sourceScope.Scope().Name())
			scope.Scope().SetVersion(sourceScope.Scope().Version())
		}
	}
	burstBStart := time.Date(2026, 8, 21, 10, 4, 0, 0, time.UTC)
	for j := 0; j < all.ResourceLogs().At(0).ScopeLogs().Len(); j++ {
		sourceScope := all.ResourceLogs().At(0).ScopeLogs().At(j)
		for k := 0; k < sourceScope.LogRecords().Len(); k++ {
			record := sourceScope.LogRecords().At(k)
			dst := first
			if time.Unix(0, int64(record.Timestamp())).After(burstBStart) {
				dst = second
			}
			record.CopyTo(dst.ResourceLogs().At(0).ScopeLogs().At(j).LogRecords().AppendEmpty())
		}
	}

	feed := func(batch plog.Logs) {
		if shuffle {
			for j := 0; j < batch.ResourceLogs().At(0).ScopeLogs().Len(); j++ {
				records := batch.ResourceLogs().At(0).ScopeLogs().At(j).LogRecords()
				rand.Shuffle(records.Len(), func(i, k int) {
					tmp := plog.NewLogRecord()
					records.At(i).CopyTo(tmp)
					records.At(k).CopyTo(records.At(i))
					tmp.CopyTo(records.At(k))
				})
			}
		}
		require.NoError(t, connectorLogs.ConsumeLogs(context.Background(), batch))
	}
	feed(first)
	time.Sleep(60 * time.Millisecond) // past reorder_window; burst A closes quiet
	feed(second)
	require.NoError(t, connectorLogs.Shutdown(context.Background())) // burst B closes shutdown
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.traces
}

func TestFixtureReplayMatchesCanonicalFixture(t *testing.T) {
	emitted := replayFixture(t, false)
	require.Len(t, emitted, 2)

	marshaler := &ptrace.JSONMarshaler{}
	actual, err := marshaler.MarshalTraces(emitted[0])
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "cursor-canonical.otlp.json"))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}

func TestFixtureReplayShuffleStable(t *testing.T) {
	plain := replayFixture(t, false)
	shuffled := replayFixture(t, true)
	require.Len(t, plain, 2)
	require.Len(t, shuffled, 2)
	marshaler := &ptrace.JSONMarshaler{}
	a, err := marshaler.MarshalTraces(plain[0])
	require.NoError(t, err)
	b, err := marshaler.MarshalTraces(shuffled[0])
	require.NoError(t, err)
	require.JSONEq(t, string(a), string(b))
}
