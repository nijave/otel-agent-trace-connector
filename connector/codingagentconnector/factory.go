// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/metadata"
)

var componentType = metadata.Type

// NewFactory creates the coding-agent connector factory.
func NewFactory() connector.Factory {
	return connector.NewFactory(
		componentType,
		func() component.Config { return createDefaultConfig() },
		connector.WithLogsToTraces(createLogsToTraces, metadata.LogsToTracesStability),
		connector.WithTracesToTraces(createTracesToTraces, metadata.TracesToTracesStability),
	)
}

func createLogsToTraces(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	next consumer.Traces,
) (connector.Logs, error) {
	return newLogsRouter(cfg.(*Config), set, next)
}

func createTracesToTraces(
	_ context.Context,
	_ connector.Settings,
	cfg component.Config,
	next consumer.Traces,
) (connector.Traces, error) {
	return newTracesRouter(cfg.(*Config), next), nil
}
