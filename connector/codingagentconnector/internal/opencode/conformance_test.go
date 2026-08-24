package opencode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestOpenCodeConformance pins the OpenCode edge to the canonical attribute
// contract: every emitted span carries the required keys, nothing outside the
// shared vocabulary survives, and every usage/latency signal present in the
// native fixture lands under its canonical counterpart — including on chat
// spans, which historically dropped the ready-made wire usage.
func TestOpenCodeConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "opencode",
		LoadRaw:   loadNativeFixtureTraces,
		Normalize: normalizeViaNormalizer,
		Signals: []canonical.Signal{
			{RawKey: "ai.usage.inputTokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "ai.usage.outputTokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "ai.usage.totalTokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
			// The wire carries the ready-made canonical input/output keys only
			// on the inner doStream span, while mapped output totals across
			// both parent and child — so a trace-wide sum would double-count.
			// Presence still pins that already-canonical wire keys survive.
			{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
			{RawKey: "ai.response.msToFirstChunk", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
		},
	})
}

// loadNativeFixtureTraces parses the OTLP JSON capture of a real OpenCode run
// into one raw Traces value.
func loadNativeFixtureTraces() (canonical.RawInput, error) {
	data, err := os.ReadFile(filepath.Join("testdata", "opencode-native-traces.json"))
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
