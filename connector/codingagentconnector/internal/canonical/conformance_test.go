// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package canonical

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

func spanWithAttrs(out ptrace.Traces, kv map[string]any) ptrace.Span {
	s := out.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	for k, v := range kv {
		switch val := v.(type) {
		case int64:
			s.Attributes().PutInt(k, val)
		case float64:
			s.Attributes().PutDouble(k, val)
		case string:
			s.Attributes().PutStr(k, val)
		}
	}
	return s
}

func contains(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func TestCheckRequired(t *testing.T) {
	e := Edge{Name: "t", Normalize: func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		_ = spanWithAttrs(out, map[string]any{"gen_ai.usage.input_tokens": int64(5)}) // missing required
		return out, nil
	}}
	errs := Check(e)
	if len(errs) == 0 || !contains(errs, "required") {
		t.Fatalf("want required-key failure, got %v", errs)
	}
}

func TestCheckAllowed(t *testing.T) {
	e := Edge{Name: "t", Normalize: func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		_ = spanWithAttrs(out, map[string]any{
			"gen_ai.operation.name":     "chat",
			"coding_agent.source":       "native",
			"coding_agent.client.name":  "x",
			"github.copilot.cost":       1.5,
			"gen_ai.usage.totalTokens2": int64(1), // unknown usage key must fail
		})
		out.ResourceSpans().At(0).Resource().Attributes().PutStr("service.name", "x")
		return out, nil
	}}
	errs := Check(e)
	if len(errs) != 2 || !contains(errs, "github.copilot.cost") || !contains(errs, "totalTokens2") {
		t.Fatalf("want exactly the two vendor-key failures, got %v", errs)
	}
}

func TestCheckSumSignal(t *testing.T) {
	raw := func() (RawInput, error) {
		in := RawInput{Traces: ptrace.NewTraces()}
		in.Traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().Attributes().PutInt("input_tokens", 10)
		return in, nil
	}
	// raw carries 10; output carries only 4 → mismatch
	e := Edge{Name: "t", LoadRaw: raw, Signals: []Signal{{RawKey: "input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: Sum}},
		Normalize: func(in RawInput) (ptrace.Traces, error) {
			out := ptrace.NewTraces()
			s := out.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
			s.Attributes().PutStr("gen_ai.operation.name", "chat")
			s.Attributes().PutStr("coding_agent.source", "native")
			s.Attributes().PutStr("coding_agent.client.name", "x")
			s.Attributes().PutInt("gen_ai.usage.input_tokens", 4)
			return out, nil
		}}
	if errs := Check(e); len(errs) == 0 || !contains(errs, "gen_ai.usage.input_tokens") {
		t.Fatalf("want sum mismatch, got %v", errs)
	}
}

func TestCheckPresenceSignal(t *testing.T) {
	rawWithTTFT := func() (RawInput, error) {
		in := RawInput{Traces: ptrace.NewTraces()}
		in.Traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().Attributes().PutInt("ttft_ms", 120)
		return in, nil
	}
	rawWithoutTTFT := func() (RawInput, error) {
		return RawInput{Traces: ptrace.NewTraces()}, nil
	}
	normalize := func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		_ = spanWithAttrs(out, map[string]any{
			"gen_ai.operation.name":    "chat",
			"coding_agent.source":      "native",
			"coding_agent.client.name": "x",
		})
		out.ResourceSpans().At(0).Resource().Attributes().PutStr("service.name", "x")
		return out, nil
	}
	sig := Signal{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: Presence}

	e := Edge{Name: "t", LoadRaw: rawWithTTFT, Normalize: normalize, Signals: []Signal{sig}}
	errs := Check(e)
	if len(errs) == 0 || !contains(errs, "time_to_first_chunk") {
		t.Fatalf("raw ttft present but output lacks canonical key: want presence failure, got %v", errs)
	}

	e.LoadRaw = rawWithoutTTFT
	if errs := Check(e); len(errs) != 0 {
		t.Fatalf("raw signal absent: want signal skipped, got %v", errs)
	}
}

func TestCheckResource(t *testing.T) {
	e := Edge{Name: "t", Normalize: func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		rs := out.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "x")
		rs.Resource().Attributes().PutStr("cursor.surface", "cli")
		s := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		s.Attributes().PutStr("gen_ai.operation.name", "chat")
		s.Attributes().PutStr("coding_agent.source", "native")
		s.Attributes().PutStr("coding_agent.client.name", "x")
		return out, nil
	}}
	errs := Check(e)
	if len(errs) != 1 || !contains(errs, "forbidden resource attribute cursor.surface") {
		t.Fatalf("want exactly the vendor-resource-key failure, got %v", errs)
	}
}

func TestCheckResourceRequiresServiceName(t *testing.T) {
	e := Edge{Name: "t", Normalize: func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		rs := out.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("telemetry.sdk.name", "opentelemetry")
		s := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		s.Attributes().PutStr("gen_ai.operation.name", "chat")
		s.Attributes().PutStr("coding_agent.source", "native")
		s.Attributes().PutStr("coding_agent.client.name", "x")
		return out, nil
	}}
	errs := Check(e)
	if len(errs) != 1 || !contains(errs, "resource missing required service.name") {
		t.Fatalf("want exactly the missing-service.name failure, got %v", errs)
	}
}

func TestFilterResource(t *testing.T) {
	out := ptrace.NewTraces()
	rs := out.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "cursor")
	rs.Resource().Attributes().PutStr("service.version", "1.2.3")
	rs.Resource().Attributes().PutStr("telemetry.sdk.language", "go")
	rs.Resource().Attributes().PutStr("cursor.surface", "cli")
	rs.Resource().Attributes().PutStr("vendor.thing", "x")
	FilterResource(rs)
	attrs := rs.Resource().Attributes()
	requireKeys := []string{"service.name", "service.version", "telemetry.sdk.language"}
	for _, key := range requireKeys {
		if _, ok := attrs.Get(key); !ok {
			t.Errorf("canonical resource key %s was stripped", key)
		}
	}
	for _, key := range []string{"cursor.surface", "vendor.thing"} {
		if _, ok := attrs.Get(key); ok {
			t.Errorf("vendor resource key %s survived FilterResource", key)
		}
	}
}

func TestIsCanonicalAttribute(t *testing.T) {
	for _, key := range canonicalAttributeKeys {
		if !IsCanonicalAttribute(key) {
			t.Errorf("enumerated canonical key rejected: %s", key)
		}
	}
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"exception.anything", true},
		{"exception.type", true},
		{"gen_ai.usage.input_tokens", true},
		{"gen_ai.usage.reasoning.output_tokens", true},
		{"gen_ai.usage.cache_read.input_tokens", true},
		{"gen_ai.usage.reasoning_tokens", false},
		{"ai.usage.inputTokens", false},
		{"event_loop.cycle_id", false},
		{"github.copilot.cost", false},
	} {
		if got := IsCanonicalAttribute(tc.key); got != tc.want {
			t.Errorf("IsCanonicalAttribute(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
	for _, key := range canonicalResourceKeys {
		if !IsCanonicalResourceKey(key) {
			t.Errorf("enumerated canonical resource key rejected: %s", key)
		}
	}
	if IsCanonicalResourceKey("cursor.surface") || IsCanonicalResourceKey("session.id") {
		t.Error("vendor or raw keys must not pass the canonical resource check")
	}
}
