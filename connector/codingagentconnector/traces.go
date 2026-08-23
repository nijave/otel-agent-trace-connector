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
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/genai"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/opencode"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/pi"
)

// tracesRouter fans the traces-to-traces edge across the stateless
// normalizers. Each normalizer claims disjoint resource groups (the GenAI
// edge defers to Claude via claude.ContainsClaudeSpans, to OpenCode via
// opencode.ContainsOpenCodeSpans, and to Pi via pi.ContainsPiSpans), so a
// group is emitted at most once and unclaimed groups stay out of the
// canonical edge.
type tracesRouter struct {
	edges []connector.Traces
	component.StartFunc
	component.ShutdownFunc
}

func newTracesRouter(next consumer.Traces) connector.Traces {
	return &tracesRouter{edges: []connector.Traces{claude.New(next), genai.New(next), opencode.New(next), pi.New(next)}}
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
