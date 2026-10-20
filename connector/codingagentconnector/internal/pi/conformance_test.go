package pi

import (
	"bytes"
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestPiConformance pins the Pi edge to the canonical attribute contract for
// both wires it supports: the flat usage.* attributes of pi-telemetry 0.1.9 and
// the serialized usage object of 0.1.28, which carries no flat usage keys.
func TestPiConformance(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		path    string
		signals []canonical.Signal
	}{
		{
			name: "pi-telemetry-0.1.9",
			path: "testdata/pi-native-traces-0.1.9.json",
			signals: []canonical.Signal{
				{RawKey: "usage.input", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage.output", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
				{RawKey: "usage.cache_read", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage.cache_write", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
				{RawKey: "stopReason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
			},
		},
		{
			name: "pi-telemetry-0.1.28",
			path: "testdata/pi-native-traces.json",
			signals: []canonical.Signal{
				{RawKey: "usage", RawField: "input", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage", RawField: "output", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "usage", RawField: "totalTokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
				{RawKey: "usage", RawField: "cacheRead", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage", RawField: "cacheWrite", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage", RawField: "reasoning", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
				{RawKey: "stopReason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
			},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			canonical.Conformance(t, canonical.Edge{
				Name:      "pi",
				LoadRaw:   func() (canonical.RawInput, error) { return loadNativeFixtureTraces(fixture.path) },
				Normalize: normalizeViaNormalizer,
				Signals:   fixture.signals,
			})
		})
	}
}

// loadNativeFixtureTraces parses a JSONL OTLP capture of a real Pi run into
// one raw Traces value.
func loadNativeFixtureTraces(path string) (canonical.RawInput, error) {
	data, err := os.ReadFile(path)
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
	if err := New(sink, true).ConsumeTraces(context.Background(), raw.Traces); err != nil {
		return ptrace.Traces{}, err
	}
	all := ptrace.NewTraces()
	for _, traces := range sink.all() {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}
