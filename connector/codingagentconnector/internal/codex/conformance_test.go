package codex

import (
	"bytes"
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestCodexConformance pins the Codex logs-to-traces edge to the canonical
// attribute contract: every emitted span carries the required keys, nothing
// outside the shared vocabulary survives, and every token counter plus the
// time-to-first-chunk latency present in the native capture lands under its
// canonical counterpart.
func TestCodexConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "codex",
		LoadRaw:   loadNativeFixtureLogs,
		Normalize: normalizeViaNormalizer,
		Signals: []canonical.Signal{
			{RawKey: "input_token_count", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "output_token_count", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "cached_token_count", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
			{RawKey: "cache_write_token_count", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			{RawKey: "tool_token_count", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
			{RawKey: "reasoning_token_count", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
			{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
		},
	})
}

// loadNativeFixtureLogs parses the JSONL plog capture of a real Codex run into
// one raw Logs value. The collector's file exporter writes one OTLP JSON export
// per line; merge the batches the way a logs backend would.
func loadNativeFixtureLogs() (canonical.RawInput, error) {
	data, err := os.ReadFile("testdata/codex-native-logs.json")
	if err != nil {
		return canonical.RawInput{}, err
	}
	unmarshaler := &plog.JSONUnmarshaler{}
	logs := plog.NewLogs()
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		batch, err := unmarshaler.UnmarshalLogs(line)
		if err != nil {
			return canonical.RawInput{}, err
		}
		batch.ResourceLogs().MoveAndAppendTo(logs.ResourceLogs())
	}
	return canonical.RawInput{Logs: logs}, nil
}

// normalizeViaNormalizer feeds raw telemetry through the full edge pipeline
// into an in-memory sink consumer. Start is never called, so Shutdown drains
// the turn synchronously instead of waiting on the sweep loop.
func normalizeViaNormalizer(raw canonical.RawInput) (ptrace.Traces, error) {
	sink := &traceSink{}
	set := connector.Settings{
		ID:                component.NewID(component.MustNewType("coding_agent")),
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
	}
	instance, err := newConnector(NewDefaultConfig(), set, sink)
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
	for _, traces := range sink.all() {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}
