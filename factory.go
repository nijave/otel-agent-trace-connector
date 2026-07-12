// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"

	"github.com/nijave/otel-agent-trace-connector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/internal/codex"
)

var componentType = component.MustNewType("coding_agent")

// NewFactory creates the coding-agent connector factory.
func NewFactory() connector.Factory {
	return connector.NewFactory(
		componentType,
		func() component.Config { return createDefaultConfig() },
		connector.WithLogsToTraces(createLogsToTraces, component.StabilityLevelDevelopment),
		connector.WithTracesToTraces(createTracesToTraces, component.StabilityLevelDevelopment),
	)
}

func createLogsToTraces(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	next consumer.Traces,
) (connector.Logs, error) {
	return codex.New(cfg.(*Config), set, next), nil
}

func createTracesToTraces(
	_ context.Context,
	_ connector.Settings,
	_ component.Config,
	next consumer.Traces,
) (connector.Traces, error) {
	return claude.New(next), nil
}
