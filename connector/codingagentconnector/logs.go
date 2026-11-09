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

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claudelogs"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
)

// logsRouter fans the logs-to-traces edge across the provider claimers. Each
// edge ignores foreign records (Codex claims codex.-prefixed event names,
// Cursor claims the cursor.telemetry scope, claudelogs claims the
// com.anthropic.claude_code.events scope's api_request/api_error records), so
// a record lands in at most one edge and unclaimed records stay out of the
// canonical edge. Start and Shutdown fan out: the codex and cursor edges own
// sweep loops and drain-on-shutdown state, while the stateless claudelogs
// edge no-ops both. The Codex edge is constructed only when the codex block's
// signals enable logs — the one duplicate implementation; cursor and
// claudelogs have no duplicate and are always constructed, so they run under
// every signals value.
type logsRouter struct {
	edges []connector.Logs
}

func newLogsRouter(cfg *Config, set connector.Settings, next consumer.Traces) (connector.Logs, error) {
	var edges []connector.Logs
	if cfg.Codex.SignalEnabled(codex.SignalLogs) {
		codexEdge, err := codex.New(cfg.Codex, cfg.CaptureIdentity, set, next)
		if err != nil {
			return nil, err
		}
		edges = append(edges, codexEdge)
	}
	cursorEdge, err := cursor.New(cfg.Cursor, cfg.CaptureIdentity, set, next)
	if err != nil {
		return nil, err
	}
	edges = append(edges, cursorEdge, claudelogs.New(cfg.ClaudeCode.ChatFromLogs(), cfg.CaptureIdentity, set, next))
	return &logsRouter{edges: edges}, nil
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
