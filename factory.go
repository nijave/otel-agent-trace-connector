// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/connector/xconnector"
	"go.opentelemetry.io/collector/consumer"
)

var componentType = component.MustNewType("coding_agent")

// NewFactory creates a logs-to-traces coding-agent connector factory.
func NewFactory() connector.Factory {
	return xconnector.NewFactory(
		componentType,
		func() component.Config { return createDefaultConfig() },
		xconnector.WithLogsToTraces(createLogsToTraces, component.StabilityLevelDevelopment),
		xconnector.WithTracesToTraces(createTracesToTraces, component.StabilityLevelDevelopment),
	)
}

func createLogsToTraces(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	next consumer.Traces,
) (connector.Logs, error) {
	return newConnector(cfg.(*Config), set, next), nil
}

func createTracesToTraces(
	_ context.Context,
	_ connector.Settings,
	_ component.Config,
	next consumer.Traces,
) (connector.Traces, error) {
	return &claudeTraceNormalizer{next: next}, nil
}
