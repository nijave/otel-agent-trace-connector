// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// buildTrace is implemented in full by the trace-construction task; the
// state-machine tests only need a callable that returns a valid batch.
func buildTrace(*burstState, string, string) ptrace.Traces {
	return ptrace.NewTraces()
}
