// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func loadFixtureTraces(t *testing.T) ptrace.Traces {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "openhands-native-traces.json"))
	require.NoError(t, err)
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(raw)
	require.NoError(t, err)
	return traces
}

// replayFixture feeds the fixture through the edge once; the stateless edge
// emits a single batch holding both trace groups.
func replayFixture(t *testing.T) ptrace.Traces {
	t.Helper()
	s := &sink{}
	require.NoError(t, New(s, true).ConsumeTraces(context.Background(), loadFixtureTraces(t)))
	require.Len(t, s.batches, 1)
	return s.batches[0]
}

func TestFixtureReplayMatchesCanonicalFixture(t *testing.T) {
	actual, err := (&ptrace.JSONMarshaler{}).MarshalTraces(replayFixture(t))
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "openhands-canonical.otlp.json"))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}

func TestFixtureReplayShuffleStable(t *testing.T) {
	plain := replayFixture(t)

	// Reverse span order within the scope and re-run through the edge.
	source := loadFixtureTraces(t)
	spans := source.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	var reversed []ptrace.Span
	for i := spans.Len() - 1; i >= 0; i-- {
		reversed = append(reversed, spans.At(i))
	}
	shuffled := normalizeOne(t, makeTraces(reversed...))

	a, err := (&ptrace.JSONMarshaler{}).MarshalTraces(plain)
	require.NoError(t, err)
	b, err := (&ptrace.JSONMarshaler{}).MarshalTraces(shuffled)
	require.NoError(t, err)
	require.JSONEq(t, string(a), string(b))
}

// Guard the fixture's own hygiene: the raw fixture really carries content
// keys, so the canonical comparison proves stripping rather than absence.
func TestRawFixtureCarriesContentThatOutputMustNot(t *testing.T) {
	spans := loadFixtureTraces(t).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	var sawContent bool
	for i := 0; i < spans.Len(); i++ {
		if _, ok := spans.At(i).Attributes().Get("gen_ai.input.messages"); ok {
			sawContent = true
		}
	}
	require.True(t, sawContent)
}
