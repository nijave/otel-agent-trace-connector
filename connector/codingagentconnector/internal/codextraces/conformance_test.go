// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestCodexTracesConformance pins the edge to the canonical contract over the
// live capture: required keys, nothing outside the vocabulary, and the
// per-call usage on the response span accounted for on chat spans.
func TestCodexTracesConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "codextraces",
		LoadRaw:   loadNativeFixtureTraces,
		Normalize: normalizeViaNormalizer,
		Signals:   codexTracesSignals(),
	})
}

// codexTracesSignals is shared with the cross-harness registry. The turn's
// totals and the response span's per-call counters both land on canonical
// keys, so sums are restricted to the response span versus chat output; the
// turn's codex.turn.* keys are checked by presence.
func codexTracesSignals() []canonical.Signal {
	return []canonical.Signal{
		{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
		{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Presence},
		{RawKey: "codex.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Presence},
		{RawKey: "codex.turn.token_usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
		{RawKey: "codex.turn.reasoning_effort", CanonicalKey: "gen_ai.request.reasoning.level", Kind: canonical.Presence},
		{RawKey: "thread.id", CanonicalKey: "gen_ai.conversation.id", Kind: canonical.Presence},
	}
}

// loadNativeFixtureTraces parses the JSONL OTLP capture of a real Codex run
// (one export per line) into one raw Traces value.
func loadNativeFixtureTraces() (canonical.RawInput, error) {
	data, err := os.ReadFile(filepath.Join("testdata", "codex-native-traces.json"))
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
