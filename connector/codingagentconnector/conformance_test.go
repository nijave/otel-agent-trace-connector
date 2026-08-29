// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

// Policy: every edge wired into traces.go or logs.go MUST be exercised by at
// least one fixture in TestCrossHarnessConformanceRegistry below. A newly
// wired edge without a fixture fails CI: the assertion matches the set of
// pipelines fixtures target, by name, against a named mirror of the router
// constructors whose length is itself tied to the routers' edge count. One
// wired pipeline may serve several fixtures (the GenAI pipeline does), so no
// count of entries ever stands in for coverage.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/genai"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/opencode"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/openhands"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/pi"
)

// conformanceSink collects emitted trace batches from any edge.
type conformanceSink struct {
	mu      sync.Mutex
	batches []ptrace.Traces
}

func (*conformanceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *conformanceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, traces)
	return nil
}

func (s *conformanceSink) all() []ptrace.Traces {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ptrace.Traces(nil), s.batches...)
}

func conformanceMerge(sink *conformanceSink) ptrace.Traces {
	all := ptrace.NewTraces()
	for _, traces := range sink.all() {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all
}

func conformanceConsumeTraces(newEdge func(consumer.Traces, bool) connector.Traces) func(canonical.RawInput) (ptrace.Traces, error) {
	return func(raw canonical.RawInput) (ptrace.Traces, error) {
		sink := &conformanceSink{}
		if err := newEdge(sink, true).ConsumeTraces(context.Background(), raw.Traces); err != nil {
			return ptrace.Traces{}, err
		}
		return conformanceMerge(sink), nil
	}
}

// conformanceConsumeLogs feeds raw logs through a stateful logs-to-traces edge;
// Start is never called, so Shutdown drains open state synchronously.
func conformanceConsumeLogs(newEdge func(consumer.Traces) (connector.Logs, error)) func(canonical.RawInput) (ptrace.Traces, error) {
	return func(raw canonical.RawInput) (ptrace.Traces, error) {
		sink := &conformanceSink{}
		instance, err := newEdge(sink)
		if err != nil {
			return ptrace.Traces{}, err
		}
		if err := instance.ConsumeLogs(context.Background(), raw.Logs); err != nil {
			return ptrace.Traces{}, err
		}
		if err := instance.Shutdown(context.Background()); err != nil {
			return ptrace.Traces{}, err
		}
		return conformanceMerge(sink), nil
	}
}

// conformanceLoadJSONLTraces parses a JSONL OTLP trace capture into one raw value.
func conformanceLoadJSONLTraces(path string) (canonical.RawInput, error) {
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

// conformanceLoadJSONTraces parses a single-document OTLP JSON capture.
func conformanceLoadJSONTraces(path string) (canonical.RawInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return canonical.RawInput{}, err
	}
	traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	if err != nil {
		return canonical.RawInput{}, err
	}
	return canonical.RawInput{Traces: traces}, nil
}

// conformanceLoadJSONLogs parses a single-document plog JSON capture.
func conformanceLoadJSONLogs(path string) (canonical.RawInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return canonical.RawInput{}, err
	}
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
	if err != nil {
		return canonical.RawInput{}, err
	}
	return canonical.RawInput{Logs: logs}, nil
}

// conformanceLoadJSONLLogs parses a JSONL plog capture into one raw value.
func conformanceLoadJSONLLogs(path string) (canonical.RawInput, error) {
	data, err := os.ReadFile(path)
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

// conformanceLoadOTLPLineTraces parses the file exporter's one-export-per-line
// format, merging batches the way a trace backend would.
func conformanceLoadOTLPLineTraces(path string) (canonical.RawInput, error) {
	file, err := os.Open(path)
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

// conformanceFixture pairs one fixture edge with the name of the wired
// pipeline (a constructor in traces.go or logs.go) that normalizes its data.
type conformanceFixture struct {
	wired string
	edge  canonical.Edge
}

// fixtureCoverageProblems reports wiring defects: a wired pipeline no fixture
// exercises, or a fixture targeting a pipeline no router wires. How many
// fixtures share a pipeline is deliberately unconstrained — the GenAI
// pipeline legitimately serves several — so counts never stand in for
// coverage.
func fixtureCoverageProblems(wired []string, fixtures []conformanceFixture) []string {
	var problems []string
	targets := make(map[string]bool, len(fixtures))
	for _, f := range fixtures {
		targets[f.wired] = true
	}
	wiredSet := make(map[string]bool, len(wired))
	for _, p := range wired {
		wiredSet[p] = true
		if !targets[p] {
			problems = append(problems, fmt.Sprintf("wired pipeline %q has no conformance fixture: add one to TestCrossHarnessConformanceRegistry", p))
		}
	}
	for p := range targets {
		if !wiredSet[p] {
			problems = append(problems, fmt.Sprintf("conformance fixture targets pipeline %q, which traces.go/logs.go do not wire", p))
		}
	}
	return problems
}

// TestCrossHarnessConformanceRegistry runs the canonical attribute contract
// over every supported harness against its native captured fixture. Each
// fixture rebuilds the same pipeline its package-local conformance test uses.
func TestCrossHarnessConformanceRegistry(t *testing.T) {
	codexNormalize := conformanceConsumeLogs(func(next consumer.Traces) (connector.Logs, error) {
		set := connector.Settings{
			ID:                component.NewID(component.MustNewType("coding_agent")),
			TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
		}
		return codex.New(codex.NewDefaultConfig(), set, next)
	})
	cursorNormalize := conformanceConsumeLogs(func(next consumer.Traces) (connector.Logs, error) {
		set := connector.Settings{
			ID:                component.NewID(component.MustNewType("coding_agent")),
			TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
		}
		return cursor.New(codex.NewDefaultConfig(), set, next)
	})

	fixtures := []conformanceFixture{
		{wired: "claude", edge: canonical.Edge{
			Name: "claude",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONLTraces("internal/claude/testdata/claude-native-traces.json")
			},
			Normalize: conformanceConsumeTraces(claude.New),
			Signals: []canonical.Signal{
				{RawKey: "input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
				{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
				{RawKey: "stop_reason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
			},
		}},
		{wired: "codex", edge: canonical.Edge{
			Name: "codex",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONLLogs("internal/codex/testdata/codex-native-logs.json")
			},
			Normalize: codexNormalize,
			Signals: []canonical.Signal{
				{RawKey: "input_token_count", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "output_token_count", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "cached_token_count", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "cache_write_token_count", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
				{RawKey: "tool_token_count", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
				{RawKey: "reasoning_token_count", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
				{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
				{RawKey: "model_reasoning_effort", CanonicalKey: "gen_ai.request.reasoning.level", Kind: canonical.Presence},
				{RawKey: "service_tier", CanonicalKey: "coding_agent.request.service_tier", Kind: canonical.Presence},
			},
		}},
		{wired: "cursor", edge: canonical.Edge{
			Name: "cursor",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONLogs("internal/cursor/testdata/cursor-native-logs.json")
			},
			Normalize: cursorNormalize,
			Signals: []canonical.Signal{
				{RawKey: "cursor.api.request.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "cursor.api.request.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "cursor.api.request.cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "cursor.api.request.cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			},
		}},
		// Three fixture edges, one wired GenAI pipeline: genai.New claims
		// disjoint resource groups per scope, so each captured emitter is a
		// fixture of its own.
		{wired: "genai", edge: canonical.Edge{
			Name: "strands",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadOTLPLineTraces("internal/genai/testdata/strands-raw.otlp.json")
			},
			Normalize: conformanceConsumeTraces(genai.New),
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.cache_read_input_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.cache_write_input_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			},
		}},
		{wired: "genai", edge: canonical.Edge{
			Name: "util-genai",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadOTLPLineTraces("internal/genai/testdata/openai-adhoc-raw.otlp.json")
			},
			Normalize: conformanceConsumeTraces(genai.New),
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			},
		}},
		{wired: "genai", edge: canonical.Edge{
			Name: "copilot-cli",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadOTLPLineTraces("internal/genai/testdata/copilot-cli-raw.otlp.json")
			},
			Normalize: conformanceConsumeTraces(genai.New),
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
		}},
		{wired: "opencode", edge: canonical.Edge{
			Name: "opencode",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONTraces("internal/opencode/testdata/opencode-native-traces.json")
			},
			Normalize: conformanceConsumeTraces(opencode.New),
			Signals: []canonical.Signal{
				// The streamText parent duplicates the doStream child's
				// counters, so sums compare the authoritative inner span
				// against chat-prefixed output spans only.
				{RawKey: "ai.usage.inputTokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum, RawSpanName: "ai.streamText.doStream", OutputSpanPrefix: "chat"},
				{RawKey: "ai.usage.outputTokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum, RawSpanName: "ai.streamText.doStream", OutputSpanPrefix: "chat"},
				{RawKey: "ai.usage.totalTokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum, RawSpanName: "ai.streamText.doStream", OutputSpanPrefix: "chat"},
				{RawKey: "ai.usage.reasoningTokens", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
				{RawKey: "ai.response.msToFirstChunk", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
			},
		}},
		{wired: "openhands", edge: canonical.Edge{
			Name: "openhands",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONTraces("internal/openhands/testdata/openhands-native-traces.json")
			},
			Normalize: conformanceConsumeTraces(openhands.New),
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "llm.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
			},
		}},
		{wired: "pi", edge: canonical.Edge{
			Name: "pi",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONLTraces("internal/pi/testdata/pi-native-traces.json")
			},
			Normalize: conformanceConsumeTraces(pi.New),
			Signals: []canonical.Signal{
				{RawKey: "usage.input", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage.output", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
				{RawKey: "usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
				{RawKey: "usage.cache_read", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
				{RawKey: "usage.cache_write", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
				{RawKey: "stopReason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
			},
		}},
	}

	// Named mirror of the edges newTracesRouter and newLogsRouter construct,
	// in construction order. The count assertion below keeps this list tied to
	// the real routers; coverage is asserted by identity, not count.
	wiredPipelines := []string{"claude", "genai", "opencode", "pi", "openhands", "codex", "cursor"}
	tracesWired := len(newTracesRouter(createDefaultConfig(), &conformanceSink{}).(*tracesRouter).edges)
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, &conformanceSink{})
	if err != nil {
		t.Fatalf("build logs router: %v", err)
	}
	logsWired := len(logsEdge.(*logsRouter).edges)
	if got := tracesWired + logsWired; got != len(wiredPipelines) {
		t.Fatalf("traces.go/logs.go construct %d edges but the named mirror holds %d pipelines: extend wiredPipelines alongside the router constructors", got, len(wiredPipelines))
	}
	for _, problem := range fixtureCoverageProblems(wiredPipelines, fixtures) {
		t.Error(problem)
	}

	for _, f := range fixtures {
		for _, violation := range canonical.Check(f.edge) {
			if f.edge.Name != f.wired {
				t.Errorf("pipeline %s [%s]: %s", f.wired, f.edge.Name, violation)
			} else {
				t.Errorf("%s", violation)
			}
		}
	}
}

// TestFixtureCoverageAcceptsMultiEdgeFixtures pins the coverage assertion to
// identity rather than counts. The real registry holds nine fixture edges over
// seven wired pipelines (the GenAI pipeline serves three fixtures), so any
// comparison between a wired-edge count and an entry or fixture count holds
// only by coincidence and breaks under harmless reshuffles; matching pipeline
// names in both directions does not.
func TestFixtureCoverageAcceptsMultiEdgeFixtures(t *testing.T) {
	fixture := func(wired, name string) conformanceFixture {
		return conformanceFixture{wired: wired, edge: canonical.Edge{Name: name}}
	}
	// Three fixtures share one wired pipeline, exactly as the registry does.
	genaiFixtures := []conformanceFixture{
		fixture("genai", "strands"),
		fixture("genai", "util-genai"),
		fixture("genai", "copilot-cli"),
	}
	if problems := fixtureCoverageProblems([]string{"genai"}, genaiFixtures); len(problems) != 0 {
		t.Fatalf("multi-fixture coverage over one wired pipeline: want no problems, got %v", problems)
	}

	for _, tc := range []struct {
		name     string
		wired    []string
		fixtures []conformanceFixture
		wantSub  string
	}{
		{
			name:     "wired pipeline without a fixture",
			wired:    []string{"genai", "pi"},
			fixtures: genaiFixtures,
			wantSub:  `"pi"`,
		},
		{
			name:     "fixture targeting an unwired pipeline",
			wired:    []string{"genai"},
			fixtures: append(genaiFixtures, fixture("ghost", "x")),
			wantSub:  `"ghost"`,
		},
	} {
		problems := fixtureCoverageProblems(tc.wired, tc.fixtures)
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.wantSub) {
			t.Errorf("%s: want a problem mentioning %s, got %v", tc.name, tc.wantSub, problems)
		}
	}
}
