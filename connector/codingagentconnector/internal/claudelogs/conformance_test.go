// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claudelogs

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

// TestClaudelogsConformance pins the Claude logs edge to the canonical
// attribute contract: every emitted span carries the required keys, nothing
// outside the shared vocabulary survives, and every usage/cost/latency
// signal present in the native fixture lands under its canonical counterpart.
func TestClaudelogsConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "claudelogs",
		LoadRaw:   loadNativeFixtureLogs,
		Normalize: normalizeViaEdge,
		Signals: []canonical.Signal{
			{RawKey: "input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
			{RawKey: "cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_write.input_tokens", Kind: canonical.Sum},
			// cost_usd is a double (and zero is a meaningful value), so sums
			// that truncate to int64 cannot check it; presence pins the map.
			{RawKey: "cost_usd", CanonicalKey: "coding_agent.usage.cost", Kind: canonical.Presence},
			{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
			{RawKey: "effort", CanonicalKey: "gen_ai.request.reasoning.level", Kind: canonical.Presence},
			{RawKey: "attempt", CanonicalKey: "coding_agent.request.attempt", Kind: canonical.Presence},
			{RawKey: "model", CanonicalKey: "gen_ai.request.model", Kind: canonical.Presence},
			{RawKey: "request_id", CanonicalKey: "gen_ai.response.id", Kind: canonical.Presence},
			{RawKey: "session.id", CanonicalKey: "gen_ai.conversation.id", Kind: canonical.Presence},
			{RawKey: "agent_id", CanonicalKey: "gen_ai.agent.id", Kind: canonical.Presence},
			{RawKey: "terminal.type", CanonicalKey: "coding_agent.terminal.type", Kind: canonical.Presence},
			{RawKey: "user.id", CanonicalKey: "coding_agent.user.id", Kind: canonical.Presence},
			{RawKey: "user.email", CanonicalKey: "coding_agent.user.email", Kind: canonical.Presence},
			{RawKey: "organization.id", CanonicalKey: "coding_agent.team.id", Kind: canonical.Presence},
		},
	})
}

// loadNativeFixtureLogs parses the single-document OTLP log capture of real
// Claude Code events (synthetic values) into one raw Logs value.
func loadNativeFixtureLogs() (canonical.RawInput, error) {
	data, err := os.ReadFile("testdata/claude-native-logs.json")
	if err != nil {
		return canonical.RawInput{}, err
	}
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
	if err != nil {
		return canonical.RawInput{}, err
	}
	return canonical.RawInput{Logs: logs}, nil
}

// normalizeViaEdge feeds raw telemetry through the full edge pipeline into an
// in-memory sink consumer.
func normalizeViaEdge(raw canonical.RawInput) (ptrace.Traces, error) {
	cfg := codex.NewDefaultConfig()
	cfg.ClaudeChatSource = codex.ClaudeChatLogs
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	sink := &conformanceSink{}
	if err := New(cfg, set, sink).ConsumeLogs(context.Background(), raw.Logs); err != nil {
		return ptrace.Traces{}, err
	}
	all := ptrace.NewTraces()
	for _, traces := range sink.traces {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}

type conformanceSink struct {
	traces []ptrace.Traces
}

func (*conformanceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *conformanceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.traces = append(s.traces, traces)
	return nil
}
