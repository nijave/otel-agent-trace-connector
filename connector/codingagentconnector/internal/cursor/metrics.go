// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/metadata"
)

// telemetry adapts the shared mdatagen instruments to the cursor edge, with
// the same lossy paths as the codex edge: bursts closed by reason, redelivered
// records dropped by dedupe, and truncated bursts.
type telemetry struct {
	builder *metadata.TelemetryBuilder
}

func (t *telemetry) recordEmitted(ctx context.Context, reason string, truncated bool) {
	t.builder.CodingAgentTurnsEmitted.Add(ctx, 1, metric.WithAttributes(attribute.String("finish_reason", reason)))
	if truncated {
		t.builder.CodingAgentTurnsTruncated.Add(ctx, 1)
	}
}

func (t *telemetry) recordDroppedRecords(ctx context.Context, n int64) {
	if n > 0 {
		t.builder.CodingAgentEventsDropped.Add(ctx, n)
	}
}
