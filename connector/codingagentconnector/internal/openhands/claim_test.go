// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	claimed := ptrace.NewTraces()
	ss := claimed.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	ss.Scope().SetName(scopeName)
	ss.Spans().AppendEmpty().SetName("conversation")
	require.True(t, Claimer.Claim(claimed.ResourceSpans().At(0)))

	other := ptrace.NewTraces()
	unmarked := other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	unmarked.Scope().SetName(scopeName)
	unmarked.Spans().AppendEmpty().SetName("generic-laminar-span")
	require.False(t, Claimer.Claim(other.ResourceSpans().At(0)))
}
