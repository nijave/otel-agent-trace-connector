package genai

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TestGenAIConformance pins the GenAI-semconv edge to the canonical attribute
// contract, once per captured emitter: every emitted span carries the required
// keys, nothing outside the shared vocabulary survives, and every token
// counter present in the raw capture lands under its canonical counterpart —
// including Strands' underscore cache variants, which must be remapped rather
// than silently stripped.
func TestGenAIConformance(t *testing.T) {
	t.Run("strands", func(t *testing.T) {
		canonical.Conformance(t, canonical.Edge{
			Name:      "strands",
			LoadRaw:   loadRawFixture("strands-raw.otlp.json"),
			Normalize: normalizeViaNormalizer,
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.cache_read_input_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.cache_write_input_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			},
		})
	})

	t.Run("util-genai", func(t *testing.T) {
		canonical.Conformance(t, canonical.Edge{
			Name:      "util-genai",
			LoadRaw:   loadRawFixture("openai-adhoc-raw.otlp.json"),
			Normalize: normalizeViaNormalizer,
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			},
		})
	})

	t.Run("copilot-cli", func(t *testing.T) {
		canonical.Conformance(t, canonical.Edge{
			Name:      "copilot-cli",
			LoadRaw:   loadRawFixture("copilot-cli-raw.otlp.json"),
			Normalize: normalizeViaNormalizer,
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.cache_read.input_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.reasoning.output_tokens", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
				// Upstream Copilot instrumentation straddles the semconv
				// cache-write rename: captures carry the old cache_creation
				// spelling, registry-aligned emitters carry the new one. Both
				// raw forms map onto the same canonical key. Signals stay
				// dormant until a capture carries the key.
				{RawKey: "gen_ai.usage.cache_creation.input_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.cache_write.input_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			},
		})
	})
}

// loadRawFixture parses the OTLP JSON capture of a live e2e run into one raw
// Traces value. The collector's file exporter writes one OTLP JSON export per
// line; merge the batches the way a trace backend would.
func loadRawFixture(name string) func() (canonical.RawInput, error) {
	return func() (canonical.RawInput, error) {
		file, err := os.Open(filepath.Join("testdata", name))
		if err != nil {
			return canonical.RawInput{}, err
		}
		defer func() { _ = file.Close() }()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		unmarshaler := &ptrace.JSONUnmarshaler{}
		merged := ptrace.NewTraces()
		for scanner.Scan() {
			batch, err := unmarshaler.UnmarshalTraces(scanner.Bytes())
			if err != nil {
				return canonical.RawInput{}, err
			}
			batch.ResourceSpans().MoveAndAppendTo(merged.ResourceSpans())
		}
		if err := scanner.Err(); err != nil {
			return canonical.RawInput{}, err
		}
		return canonical.RawInput{Traces: merged}, nil
	}
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
