// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	claimed := ptrace.NewTraces()
	claimed.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Scope().SetName(scopePrefix)
	require.True(t, Claimer.Claim(claimed.ResourceSpans().At(0)))

	other := ptrace.NewTraces()
	other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Scope().SetName("not-pi")
	require.False(t, Claimer.Claim(other.ResourceSpans().At(0)))
}
