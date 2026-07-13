package codingagentconnector

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/connector/connectortest"
	"go.opentelemetry.io/collector/consumer/consumertest"
)

func TestFactoryCreatesSupportedEdges(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig()
	settings := connectortest.NewNopSettings(componentType)

	logs, err := factory.CreateLogsToTraces(t.Context(), settings, cfg, consumertest.NewNop())
	require.NoError(t, err)
	require.NotNil(t, logs)

	traces, err := factory.CreateTracesToTraces(t.Context(), settings, cfg, consumertest.NewNop())
	require.NoError(t, err)
	require.NotNil(t, traces)
}

func TestFactoryDefaultConfigConformsToCollectorRules(t *testing.T) {
	require.NoError(t, componenttest.CheckConfigStruct(NewFactory().CreateDefaultConfig()))
}
