// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
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

	// Reverse span order within the scope and re-run through the edge. The
	// shuffled input keeps the fixture's own resource and scope, so only the
	// span order differs from the replay above.
	source := loadFixtureTraces(t)
	srcRS := source.ResourceSpans().At(0)
	shuffledInput := ptrace.NewTraces()
	rs := shuffledInput.ResourceSpans().AppendEmpty()
	srcRS.Resource().CopyTo(rs.Resource())
	ss := rs.ScopeSpans().AppendEmpty()
	srcRS.ScopeSpans().At(0).Scope().CopyTo(ss.Scope())
	srcSpans := srcRS.ScopeSpans().At(0).Spans()
	for i := srcSpans.Len() - 1; i >= 0; i-- {
		srcSpans.At(i).CopyTo(ss.Spans().AppendEmpty())
	}
	shuffled := normalizeOne(t, shuffledInput)

	a, err := (&ptrace.JSONMarshaler{}).MarshalTraces(plain)
	require.NoError(t, err)
	b, err := (&ptrace.JSONMarshaler{}).MarshalTraces(shuffled)
	require.NoError(t, err)
	require.JSONEq(t, string(a), string(b))
}

// Guard the fixture's own hygiene: the raw fixture really carries the wire's
// content keys, so the canonical replay proves stripping rather than absence.
func TestRawFixtureCarriesContentThatOutputMustNot(t *testing.T) {
	contentKeys := []string{"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.definitions"}
	rawSpans := loadFixtureTraces(t).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for _, key := range contentKeys {
		var found bool
		for i := 0; i < rawSpans.Len() && !found; i++ {
			_, found = rawSpans.At(i).Attributes().Get(key)
		}
		require.Truef(t, found, "raw fixture must carry %s so its stripping is proven", key)
	}

	// Canonical output is allowlist-only: neither the content keys nor any
	// vendor (lmnr., gen_ai.span.) key may survive the replay.
	var leaked []string
	for _, span := range allSpansOf(replayFixture(t)) {
		span.Attributes().Range(func(key string, _ pcommon.Value) bool {
			if strings.HasPrefix(key, "lmnr.") || strings.HasPrefix(key, "gen_ai.span.") {
				leaked = append(leaked, key)
				return true
			}
			for _, banned := range contentKeys {
				if key == banned {
					leaked = append(leaked, key)
				}
			}
			return true
		})
	}
	require.Empty(t, leaked, "vendor and content keys must not survive normalization")
}
