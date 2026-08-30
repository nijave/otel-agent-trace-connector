package openhands

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestOpenHandsConformance pins the OpenHands edge to the canonical attribute
// contract: every emitted span carries the required keys, nothing outside the
// shared vocabulary survives, and every usage signal present in the native
// fixture lands under its canonical counterpart.
func TestOpenHandsConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "openhands",
		LoadRaw:   loadNativeFixtureTraces,
		Normalize: normalizeViaNormalizer,
		Signals: []canonical.Signal{
			{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "llm.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
		},
	})
}

// loadNativeFixtureTraces parses the JSON OTLP capture of a real OpenHands
// run into one raw Traces value.
func loadNativeFixtureTraces() (canonical.RawInput, error) {
	data, err := os.ReadFile("testdata/openhands-native-traces.json")
	if err != nil {
		return canonical.RawInput{}, err
	}
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	if err != nil {
		return canonical.RawInput{}, err
	}
	return canonical.RawInput{Traces: traces}, nil
}

// normalizeViaNormalizer feeds raw telemetry through the full edge pipeline
// into an in-memory sink consumer.
func normalizeViaNormalizer(raw canonical.RawInput) (ptrace.Traces, error) {
	s := &sink{}
	if err := New(s, true).ConsumeTraces(context.Background(), raw.Traces); err != nil {
		return ptrace.Traces{}, err
	}
	all := ptrace.NewTraces()
	for _, traces := range s.batches {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}
