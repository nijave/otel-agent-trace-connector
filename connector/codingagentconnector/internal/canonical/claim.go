// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package canonical

import "go.opentelemetry.io/collector/pdata/ptrace"

// Claimer reports whether a harness edge owns a resource group. Each harness
// edge that other edges defer to (claude, opencode, pi, openhands,
// codextraces) exports one as Claimer. An edge that must leave another
// edge's groups alone (the GenAI edge) calls that harness's Claimer, which is
// the only identifier one harness package may use from another
// (TestNoCrossHarnessImports).
type Claimer interface {
	Claim(ptrace.ResourceSpans) bool
}
