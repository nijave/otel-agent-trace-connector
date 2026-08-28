package pi

import (
	"bytes"
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestPiConformance pins the Pi edge to the canonical attribute contract:
// every emitted span carries the required keys, nothing outside the shared
// vocabulary survives, and every usage/stop-reason signal present in the
// native fixture lands under its canonical counterpart.
func TestPiConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "pi",
		LoadRaw:   loadNativeFixtureTraces,
		Normalize: normalizeViaNormalizer,
		Signals: []canonical.Signal{
			{RawKey: "usage.input", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "usage.output", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
			{RawKey: "usage.cache_read", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
			{RawKey: "usage.cache_write", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			{RawKey: "stopReason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
		},
	})
}

// loadNativeFixtureTraces parses the JSONL OTLP capture of a real Pi run into
// one raw Traces value.
func loadNativeFixtureTraces() (canonical.RawInput, error) {
	data, err := os.ReadFile("testdata/pi-native-traces.json")
	if err != nil {
		return canonical.RawInput{}, err
	}
	unmarshaler := &ptrace.JSONUnmarshaler{}
	traces := ptrace.NewTraces()
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		batch, err := unmarshaler.UnmarshalTraces(line)
		if err != nil {
			return canonical.RawInput{}, err
		}
		batch.ResourceSpans().MoveAndAppendTo(traces.ResourceSpans())
	}
	return canonical.RawInput{Traces: traces}, nil
}

// normalizeViaNormalizer feeds raw telemetry through the full edge pipeline
// into an in-memory sink consumer.
func normalizeViaNormalizer(raw canonical.RawInput) (ptrace.Traces, error) {
	sink := &traceSink{}
	if err := New(sink).ConsumeTraces(context.Background(), raw.Traces); err != nil {
		return ptrace.Traces{}, err
	}
	all := ptrace.NewTraces()
	for _, traces := range sink.all() {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}
