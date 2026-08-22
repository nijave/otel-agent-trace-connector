// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"
	"errors"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
)

// logsRouter fans the logs-to-traces edge across the provider claimers. Each
// edge ignores foreign records (Codex claims codex.-prefixed event names,
// Cursor claims the cursor.telemetry scope), so a record lands in at most one
// edge and unclaimed records stay out of the canonical edge. Unlike the
// stateless tracesRouter, Start and Shutdown fan out: both edges own sweep
// loops and drain-on-shutdown state.
type logsRouter struct {
	edges []connector.Logs
}

func newLogsRouter(cfg *Config, set connector.Settings, next consumer.Traces) (connector.Logs, error) {
	codexEdge, err := codex.New(cfg, set, next)
	if err != nil {
		return nil, err
	}
	cursorEdge, err := cursor.New(cfg, set, next)
	if err != nil {
		return nil, err
	}
	return &logsRouter{edges: []connector.Logs{codexEdge, cursorEdge}}, nil
}

func (*logsRouter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (r *logsRouter) Start(ctx context.Context, host component.Host) error {
	for _, edge := range r.edges {
		if err := edge.Start(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

func (r *logsRouter) Shutdown(ctx context.Context) error {
	var errs error
	for _, edge := range r.edges {
		errs = errors.Join(errs, edge.Shutdown(ctx))
	}
	return errs
}

func (r *logsRouter) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	for _, edge := range r.edges {
		if err := edge.ConsumeLogs(ctx, logs); err != nil {
			return err
		}
	}
	return nil
}

var _ connector.Logs = (*logsRouter)(nil)
