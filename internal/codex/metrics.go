// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/nijave/otel-agent-trace-connector/internal/metadata"
)

// telemetry adapts the mdatagen-generated TelemetryBuilder to the connector's
// lossy paths: turns finalized by reason (including bounded-state eviction and
// inactivity timeout), redelivered events dropped by within-turn deduplication,
// and turns whose events were truncated. The active-turn count is reported by an
// observable gauge registered in newConnector.
type telemetry struct {
	builder *metadata.TelemetryBuilder
}

// recordEmitted counts a finalized turn by its finish reason and, when the turn
// dropped events under max_events_per_turn, records the truncation.
func (t *telemetry) recordEmitted(ctx context.Context, reason string, truncated bool) {
	t.builder.CodingAgentTurnsEmitted.Add(ctx, 1, metric.WithAttributes(attribute.String("finish_reason", reason)))
	if truncated {
		t.builder.CodingAgentTurnsTruncated.Add(ctx, 1)
	}
}

func (t *telemetry) recordDroppedEvents(ctx context.Context, n int64) {
	if n > 0 {
		t.builder.CodingAgentEventsDropped.Add(ctx, n)
	}
}
