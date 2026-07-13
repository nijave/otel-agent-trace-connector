// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// telemetry holds the connector's self-observability instruments. They make the
// otherwise-silent lossy paths visible to operators: turns finalized by reason
// (including bounded-state eviction and inactivity timeout), redelivered events
// dropped by within-turn deduplication, turns whose events were truncated, and
// the live active-turn count against the configured bound.
type telemetry struct {
	turnsEmitted   metric.Int64Counter
	eventsDropped  metric.Int64Counter
	turnsTruncated metric.Int64Counter
	activeTurns    metric.Int64ObservableGauge
}

func newTelemetry(meter metric.Meter) (*telemetry, error) {
	t := &telemetry{}
	var err error
	if t.turnsEmitted, err = meter.Int64Counter(
		"coding_agent_turns_emitted",
		metric.WithDescription("Reconstructed coding-agent turns emitted downstream, labeled by finish reason."),
		metric.WithUnit("1"),
	); err != nil {
		return nil, err
	}
	if t.eventsDropped, err = meter.Int64Counter(
		"coding_agent_events_dropped",
		metric.WithDescription("Redelivered Codex events dropped by within-turn deduplication."),
		metric.WithUnit("1"),
	); err != nil {
		return nil, err
	}
	if t.turnsTruncated, err = meter.Int64Counter(
		"coding_agent_turns_truncated",
		metric.WithDescription("Emitted turns that exceeded max_events_per_turn and dropped events."),
		metric.WithUnit("1"),
	); err != nil {
		return nil, err
	}
	if t.activeTurns, err = meter.Int64ObservableGauge(
		"coding_agent_active_turns",
		metric.WithDescription("Codex turns currently held in correlation state."),
		metric.WithUnit("1"),
	); err != nil {
		return nil, err
	}
	return t, nil
}

// recordEmitted counts a finalized turn by its finish reason and, when the turn
// dropped events under max_events_per_turn, records the truncation.
func (t *telemetry) recordEmitted(ctx context.Context, reason string, truncated bool) {
	t.turnsEmitted.Add(ctx, 1, metric.WithAttributes(attribute.String("finish_reason", reason)))
	if truncated {
		t.turnsTruncated.Add(ctx, 1)
	}
}

func (t *telemetry) recordDroppedEvents(ctx context.Context, n int64) {
	if n > 0 {
		t.eventsDropped.Add(ctx, n)
	}
}
