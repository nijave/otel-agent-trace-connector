// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// loadFixtureTraces merges the JSONL capture (one OTLP export per line) the
// way a trace backend would; the conformance test shares the loader.
func loadFixtureTraces(t *testing.T) ptrace.Traces {
	t.Helper()
	raw, err := loadNativeFixtureTraces()
	require.NoError(t, err)
	return raw.Traces
}

func replayFixture(t *testing.T) ptrace.Traces {
	t.Helper()
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), loadFixtureTraces(t)))
	require.Len(t, sink.all(), 1)
	return sink.all()[0]
}

func TestFixtureReplayMatchesCanonicalFixture(t *testing.T) {
	actual, err := (&ptrace.JSONMarshaler{}).MarshalTraces(replayFixture(t))
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "codex-traces-canonical.otlp.json"))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}

// TestFixtureReplayShape pins what the live capture must yield: one root per
// turn, a chat child with usage and the turn's model, and at least one
// execute_tool child, with nothing outside the vocabulary.
func TestFixtureReplayShape(t *testing.T) {
	out := replayFixture(t)
	roots, chats, tools := 0, 0, 0
	for i := 0; i < out.ResourceSpans().Len(); i++ {
		spans := out.ResourceSpans().At(i).ScopeSpans().At(0).Spans()
		for j := 0; j < spans.Len(); j++ {
			span := spans.At(j)
			switch attrString(t, span, "gen_ai.operation.name") {
			case "invoke_agent":
				roots++
				require.NotEmpty(t, attrString(t, span, "gen_ai.conversation.id"))
			case "chat":
				chats++
				require.NotEmpty(t, attrString(t, span, "gen_ai.request.model"))
				attrInt(t, span, "gen_ai.usage.input_tokens")
			case "execute_tool":
				tools++
			}
			requireOnlyCanonical(t, span)
		}
	}
	require.GreaterOrEqual(t, roots, 1)
	require.GreaterOrEqual(t, chats, 1)
	require.GreaterOrEqual(t, tools, 1)
}
