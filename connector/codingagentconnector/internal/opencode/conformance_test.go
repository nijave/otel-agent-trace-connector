package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// allSpans flattens a Traces value into its spans.
func allSpans(traces ptrace.Traces) []ptrace.Span {
	var result []ptrace.Span
	rss := traces.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				result = append(result, spans.At(k))
			}
		}
	}
	return result
}

// openCodeSignals is the wire contract under test: each model call's usage
// lands exactly once on its chat span. The streamText parent duplicates the
// doStream child's counters on the wire, so sums compare per span — raw
// against the authoritative inner span, output against chat-prefixed spans.
func openCodeSignals() []canonical.Signal {
	return []canonical.Signal{
		{RawKey: "ai.usage.inputTokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum, RawSpanName: "ai.streamText.doStream", OutputSpanPrefix: "chat"},
		{RawKey: "ai.usage.outputTokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum, RawSpanName: "ai.streamText.doStream", OutputSpanPrefix: "chat"},
		{RawKey: "ai.usage.totalTokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum, RawSpanName: "ai.streamText.doStream", OutputSpanPrefix: "chat"},
		{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
		{RawKey: "ai.response.msToFirstChunk", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
	}
}

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
		Signals:   openCodeSignals(),
	})
}

// TestOpenCodeConformanceDetectsPartialTokenMapping proves the restored sum
// guarantee fails closed: a normalizer that drops one token counter from the
// chat span violates the contract again, which the former presence-only
// checks let pass.
func TestOpenCodeConformanceDetectsPartialTokenMapping(t *testing.T) {
	dropOutputTokens := func(raw canonical.RawInput) (ptrace.Traces, error) {
		out, err := normalizeViaNormalizer(raw)
		if err != nil {
			return ptrace.Traces{}, err
		}
		for _, span := range allSpans(out) {
			if strings.HasPrefix(span.Name(), "chat") {
				span.Attributes().Remove("gen_ai.usage.output_tokens")
			}
		}
		return out, nil
	}
	violations := canonical.Check(canonical.Edge{
		Name:      "opencode",
		LoadRaw:   loadNativeFixtureTraces,
		Normalize: dropOutputTokens,
		Signals:   openCodeSignals(),
	})
	require.Contains(t, strings.Join(violations, "\n"),
		"signal ai.usage.outputTokens sum mismatch for gen_ai.usage.output_tokens")
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
