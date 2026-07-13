package codex

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.uber.org/goleak"
	"go.uber.org/zap"
)

// TestMain fails the package's tests if any goroutine outlives them. The
// connector owns a background sweep loop started in Start; a Shutdown that fails
// to reap it (or a test that forgets to Shutdown) surfaces here rather than as a
// slow production leak.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestStartShutdownReapsSweepLoop(t *testing.T) {
	defer goleak.VerifyNone(t)
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	instance := newTestConnector(t, NewDefaultConfig(), set, &traceSink{})
	require.NoError(t, instance.Start(context.Background(), nil))
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeLogs(
		testEvent("codex.user_prompt", time.Now(), nil),
	)))
	require.NoError(t, instance.Shutdown(context.Background()))
}
