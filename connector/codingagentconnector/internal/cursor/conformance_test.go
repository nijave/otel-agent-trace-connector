package cursor

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

// TestCursorConformance pins the Cursor logs-to-traces edge to the canonical
// attribute contract: every emitted span carries the required keys, nothing
// outside the shared vocabulary survives (event names excepted — they are not
// attributes), and every token counter present in the native capture lands
// under its canonical counterpart.
func TestCursorConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "cursor",
		LoadRaw:   loadNativeFixtureLogs,
		Normalize: normalizeViaNormalizer,
		Signals: []canonical.Signal{
			{RawKey: "cursor.api.request.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "cursor.api.request.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "cursor.api.request.cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
			{RawKey: "cursor.api.request.cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
		},
	})
}

// loadNativeFixtureLogs parses the captured plog export of a real Cursor run
// into one raw Logs value.
func loadNativeFixtureLogs() (canonical.RawInput, error) {
	data, err := os.ReadFile("testdata/cursor-native-logs.json")
	if err != nil {
		return canonical.RawInput{}, err
	}
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
	if err != nil {
		return canonical.RawInput{}, err
	}
	return canonical.RawInput{Logs: logs}, nil
}

// normalizeViaNormalizer feeds raw telemetry through the full edge pipeline
// into an in-memory sink consumer. Start is never called, so Shutdown drains
// every open burst synchronously instead of waiting on the sweep loop.
func normalizeViaNormalizer(raw canonical.RawInput) (ptrace.Traces, error) {
	sink := &traceSink{}
	set := connector.Settings{
		ID:                component.NewID(component.MustNewType("coding_agent")),
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
	}
	instance, err := New(codex.NewDefaultConfig(), set, sink)
	if err != nil {
		return ptrace.Traces{}, err
	}
	if err := instance.ConsumeLogs(context.Background(), raw.Logs); err != nil {
		return ptrace.Traces{}, err
	}
	if err := instance.Shutdown(context.Background()); err != nil {
		return ptrace.Traces{}, err
	}
	all := ptrace.NewTraces()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, traces := range sink.traces {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}
