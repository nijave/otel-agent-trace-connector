// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codextraces"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/genai"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/opencode"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/openhands"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/pi"
)

// tracesRouter fans the traces-to-traces edge across the stateless
// normalizers. Each normalizer claims disjoint resource groups (the GenAI
// edge defers to Claude, OpenCode, Pi, and OpenHands through each package's
// Claimer; the OpenHands edge claims lmnr.tracer scope groups carrying
// OpenHands markers, and the Codex native edge claims groups whose scope
// equals the service name and holds a Codex marker), so a group is emitted at
// most once and unclaimed groups stay out of the canonical edge. The Codex
// native edge is constructed only when the codex block's signals enable
// traces — the one duplicate implementation; the other normalizers have no
// duplicate and are always constructed.
type tracesRouter struct {
	edges []connector.Traces
	component.StartFunc
	component.ShutdownFunc
}

func newTracesRouter(cfg *Config, next consumer.Traces) connector.Traces {
	id := cfg.CaptureIdentity
	edges := []connector.Traces{
		// The Claude edge hands chat spans to the claudelogs edge under
		// claude_code.chat_source: logs, dropping claude_code.llm_request
		// instead.
		claude.NewWithChatSource(next, id, cfg.ClaudeCode.ChatFromLogs()), genai.New(next, id), opencode.New(next, id), pi.New(next, id), openhands.New(next, id),
	}
	if cfg.Codex.SignalEnabled(codex.SignalTraces) {
		edges = append(edges, codextraces.New(next, id))
	}
	return &tracesRouter{edges: edges}
}

func (*tracesRouter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (r *tracesRouter) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	for _, edge := range r.edges {
		if err := edge.ConsumeTraces(ctx, traces); err != nil {
			return err
		}
	}
	return nil
}

var _ connector.Traces = (*tracesRouter)(nil)
