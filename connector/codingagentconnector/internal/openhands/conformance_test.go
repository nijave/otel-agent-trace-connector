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
			// The `llm.<model>` wrapper writes the cache counters the inner
			// `litellm.completion` span also carries; the edge folds the wrapper
			// away, so the inner span is the authoritative source and the
			// trace-wide total would double count.
			{RawKey: "gen_ai.usage.cache_read_input_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum, RawSpanName: "litellm.completion"},
			{RawKey: "gen_ai.usage.cache_creation_input_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum, RawSpanName: "litellm.completion"},
			{RawKey: "gen_ai.request.reasoning_effort", CanonicalKey: "gen_ai.request.reasoning.level", Kind: canonical.Presence},
			{RawKey: "gen_ai.response.id", CanonicalKey: "gen_ai.response.id", Kind: canonical.Presence},
			{RawKey: "gen_ai.response.model", CanonicalKey: "gen_ai.response.model", Kind: canonical.Presence},
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
