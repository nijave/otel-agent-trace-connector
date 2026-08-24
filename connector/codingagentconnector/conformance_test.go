// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

// Policy: every supported harness wired into traces.go or logs.go MUST have an
// entry in TestCrossHarnessConformanceRegistry below. A newly wired edge
// without an entry here fails CI: the wiring assertion compares the number of
// edges the routers construct against the registry length.

import (
	"bufio"
	"bytes"
	"context"
	"os"
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

func conformanceConsumeTraces(newEdge func(consumer.Traces) connector.Traces) func(canonical.RawInput) (ptrace.Traces, error) {
	return func(raw canonical.RawInput) (ptrace.Traces, error) {
		sink := &conformanceSink{}
		if err := newEdge(sink).ConsumeTraces(context.Background(), raw.Traces); err != nil {
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

// TestCrossHarnessConformanceRegistry runs the canonical attribute contract
// over every supported harness against its native captured fixture. Each entry
// rebuilds the same pipeline its package-local conformance test uses.
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

	registry := []struct {
		name  string
		edges []canonical.Edge
	}{
		{
			name: "claude",
			edges: []canonical.Edge{{
				Name: "claude",
				LoadRaw: func() (canonical.RawInput, error) {
					return conformanceLoadJSONLTraces("internal/claude/testdata/claude-native-traces.json")
				},
				Normalize: conformanceConsumeTraces(claude.New),
				Signals: []canonical.Signal{
					{RawKey: "input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
					{RawKey: "output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
					{RawKey: "cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
					{RawKey: "cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
					{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
					{RawKey: "stop_reason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
				},
			}},
		},
		{
			name: "codex",
			edges: []canonical.Edge{{
				Name: "codex",
				LoadRaw: func() (canonical.RawInput, error) {
					return conformanceLoadJSONLLogs("internal/codex/testdata/codex-native-logs.json")
				},
				Normalize: codexNormalize,
				Signals: []canonical.Signal{
					{RawKey: "input_token_count", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
					{RawKey: "output_token_count", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
					{RawKey: "cached_token_count", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
					{RawKey: "tool_token_count", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
					{RawKey: "reasoning_token_count", CanonicalKey: "gen_ai.usage.reasoning.output_tokens", Kind: canonical.Sum},
					{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
				},
			}},
		},
		{
			name: "cursor",
			edges: []canonical.Edge{{
				Name: "cursor",
				LoadRaw: func() (canonical.RawInput, error) {
					return conformanceLoadJSONLogs("internal/cursor/testdata/cursor-native-logs.json")
				},
				Normalize: cursorNormalize,
				Signals: []canonical.Signal{
					{RawKey: "cursor.api.request.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
					{RawKey: "cursor.api.request.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
					{RawKey: "cursor.api.request.cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
					{RawKey: "cursor.api.request.cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
				},
			}},
		},
		{
			name: "genai-scopes",
			edges: []canonical.Edge{
				{
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
						{RawKey: "gen_ai.usage.cache_write_input_tokens", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
					},
				},
				{
					Name: "util-genai",
					LoadRaw: func() (canonical.RawInput, error) {
						return conformanceLoadOTLPLineTraces("internal/genai/testdata/openai-adhoc-raw.otlp.json")
					},
					Normalize: conformanceConsumeTraces(genai.New),
					Signals: []canonical.Signal{
						{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
						{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
					},
				},
				{
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
					},
				},
			},
		},
		{
			name: "opencode",
			edges: []canonical.Edge{{
				Name: "opencode",
				LoadRaw: func() (canonical.RawInput, error) {
					return conformanceLoadJSONTraces("internal/opencode/testdata/opencode-native-traces.json")
				},
				Normalize: conformanceConsumeTraces(opencode.New),
				Signals: []canonical.Signal{
					{RawKey: "ai.usage.inputTokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
					{RawKey: "ai.usage.outputTokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
					{RawKey: "ai.usage.totalTokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Sum},
					{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
					{RawKey: "ai.response.msToFirstChunk", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
				},
			}},
		},
		{
			name: "openhands",
			edges: []canonical.Edge{{
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
		},
		{
			name: "pi",
			edges: []canonical.Edge{{
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
					{RawKey: "usage.cache_write", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
					{RawKey: "stopReason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
				},
			}},
		},
	}

	// No duplicated or missing harness names in the hardcoded set.
	want := []string{"claude", "codex", "cursor", "genai-scopes", "opencode", "openhands", "pi"}
	seen := make(map[string]bool, len(registry))
	for _, entry := range registry {
		if seen[entry.name] {
			t.Fatalf("duplicate conformance registry entry for harness %q", entry.name)
		}
		seen[entry.name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("harness %q missing from the conformance registry: register conformance", name)
		}
	}

	// Any wired edge without a registry entry fails CI: compare the number of
	// edges the routers construct against the registry size.
	tracesWired := len(newTracesRouter(&conformanceSink{}).(*tracesRouter).edges)
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, &conformanceSink{})
	if err != nil {
		t.Fatalf("build logs router: %v", err)
	}
	logsWired := len(logsEdge.(*logsRouter).edges)
	if got := tracesWired + logsWired; got != len(registry) {
		t.Errorf("wired %d edges but the conformance registry holds %d entries: register conformance for every wired harness", got, len(registry))
	}

	for _, entry := range registry {
		for _, edge := range entry.edges {
			for _, violation := range canonical.Check(edge) {
				if edge.Name != entry.name {
					t.Errorf("harness %s [%s]: %s", entry.name, edge.Name, violation)
				} else {
					t.Errorf("%s", violation)
				}
			}
		}
	}
}
