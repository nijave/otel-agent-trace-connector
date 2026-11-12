// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	input := ptrace.NewTraces()
	addTurn(newCodexGroup(input))
	require.True(t, Claimer.Claim(input.ResourceSpans().At(0)))

	noMarker := ptrace.NewTraces()
	addSpan(newCodexGroup(noMarker), "run_turn", 1, 0, 0, time.Second)
	require.False(t, Claimer.Claim(noMarker.ResourceSpans().At(0)))
}
