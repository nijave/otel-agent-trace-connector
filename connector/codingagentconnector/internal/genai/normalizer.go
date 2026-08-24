// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package genai normalizes GenAI-semantic-convention native traces (the
// opentelemetry-instrumentation-openai-v2 package in both semconv modes,
// direct opentelemetry-util-genai users, and Strands Agents SDK) into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change, and canonical
// output is restricted to an allowlist of benign attributes and events, so
// content never reaches it — not even from unknown vendor namespaces.
// Scopes outside the allowlist in a claimed group are dropped, so canonical
// output carries only coding-agent spans.
package genai

import (
	"context"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/content"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/opencode"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/openhands"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/pi"
)

// scopePrefixes lists the instrumentation-scope names this edge claims.
// Prefixes rather than exact names: upstream is renaming
// opentelemetry-instrumentation-openai-v2 to
// opentelemetry-instrumentation-genai-openai, and util-genai emits from a
// module whose path may shift below opentelemetry.util.genai.
var scopePrefixes = []string{
	"opentelemetry.instrumentation.openai_v2",
	"opentelemetry.util.genai",
	"opentelemetry.instrumentation.genai",
	"strands.telemetry",
	// GitHub Copilot CLI / VS Code Chat; prefix form tolerates sub-scopes.
	"github.copilot",
}

type genAITraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless GenAI-semconv traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &genAITraceNormalizer{next: next}
}

func (*genAITraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *genAITraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		// Claude, OpenCode, Pi, and OpenHands groups belong to their own
		// normalizers even when they also carry GenAI scopes; claiming here
		// would emit each group twice.
		if claude.ContainsClaudeSpans(inputResourceSpans) {
			continue
		}
		if opencode.ContainsOpenCodeSpans(inputResourceSpans) {
			continue
		}
		if pi.ContainsPiSpans(inputResourceSpans) {
			continue
		}
		if openhands.ContainsOpenHandsSpans(inputResourceSpans) {
			continue
		}
		if !containsGenAIScopes(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.CopyTo(rs)
		serviceName := resourceString(rs.Resource(), "service.name")
		serviceVersion := resourceString(rs.Resource(), "service.version")
		// Canonical output carries only coding-agent spans: scopes outside
		// the allowlist in a claimed group are dropped here (the raw
		// pipelines preserve the originals).
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool {
			return !matchesGenAIScope(ss.Scope().Name())
		})
		canonical.FilterResource(rs)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j)
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				normalizeSpan(span, ss.Scope().Name(), serviceName, serviceVersion)
				content.Strip(span)
			}
		}
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

func containsGenAIScopes(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		if matchesGenAIScope(resourceSpans.ScopeSpans().At(i).Scope().Name()) {
			return true
		}
	}
	return false
}

func matchesGenAIScope(name string) bool {
	for _, prefix := range scopePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// nameSubjectByOperation maps canonical operations to the attribute that
// supplies the span-name subject ({operation} {subject}).
var nameSubjectByOperation = map[string]string{
	"chat":         "gen_ai.request.model",
	"invoke_agent": "gen_ai.agent.name",
	"execute_tool": "gen_ai.tool.name",
}

func normalizeSpan(span ptrace.Span, scopeName, serviceName, serviceVersion string) {
	attrs := span.Attributes()
	operationValue, ok := attrs.Get("gen_ai.operation.name")
	if !ok {
		return
	}
	operation := operationValue.Str()
	if subjectKey, canonical := nameSubjectByOperation[operation]; canonical {
		if subjectValue, ok := attrs.Get(subjectKey); ok && subjectValue.Str() != "" {
			span.SetName(operation + " " + subjectValue.Str())
		}
	}
	if _, ok := attrs.Get("gen_ai.provider.name"); !ok {
		if systemValue, ok := attrs.Get("gen_ai.system"); ok {
			// Extract before Put: a map write may invalidate held values.
			provider := systemValue.Str()
			if provider != "" {
				attrs.PutStr("gen_ai.provider.name", provider)
			}
		}
	}
	attrs.Remove("gen_ai.system")
	mapLegacyTokens(attrs, "gen_ai.usage.prompt_tokens", "gen_ai.usage.input_tokens")
	mapLegacyTokens(attrs, "gen_ai.usage.completion_tokens", "gen_ai.usage.output_tokens")
	remapUsageKeys(attrs)
	restrictUsageKeys(attrs)
	mapTTFT(attrs)
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.source.scope", scopeName)
	if serviceName != "" {
		attrs.PutStr("coding_agent.client.name", serviceName)
	}
	if serviceVersion != "" {
		attrs.PutStr("coding_agent.client.version", serviceVersion)
	}
}

// mapLegacyTokens copies a legacy usage attribute onto the current key when
// the current key is absent, then drops the legacy key from canonical output.
func mapLegacyTokens(attrs pcommon.Map, legacyKey, currentKey string) {
	count, ok := intAttr(attrs, legacyKey)
	if !ok {
		return
	}
	if _, exists := attrs.Get(currentKey); !exists {
		attrs.PutInt(currentKey, count)
	}
	attrs.Remove(legacyKey)
}

// usageRemaps maps emitter-specific usage spellings onto their canonical
// dotted keys. Strands Agents SDK emits cache counters with underscores where
// the semconv uses a nested segment.
var usageRemaps = []struct{ raw, canonical string }{
	{"gen_ai.usage.cache_read_input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"gen_ai.usage.cache_write_input_tokens", "gen_ai.usage.cache_creation.input_tokens"},
}

// allowedUsageKeys enumerates the entire canonical usage vocabulary. There is
// deliberately no gen_ai.usage. wildcard: any other member of that namespace
// is a vendor key and is removed here rather than relying on the strip.
var allowedUsageKeys = map[string]bool{
	"gen_ai.usage.input_tokens":                true,
	"gen_ai.usage.output_tokens":               true,
	"gen_ai.usage.total_tokens":                true,
	"gen_ai.usage.cache_read.input_tokens":     true,
	"gen_ai.usage.cache_creation.input_tokens": true,
	"gen_ai.usage.reasoning.output_tokens":     true,
}

// remapUsageKeys copies Strands' underscore cache variants onto their dotted
// counterparts (the dotted key wins when both are present) and removes the
// raw keys. Values coerce through intAttr so a double- or string-typed
// counter still lands on its canonical key instead of being deleted unmapped;
// only genuinely malformed values drop.
func remapUsageKeys(attrs pcommon.Map) {
	for _, remap := range usageRemaps {
		count, ok := intAttr(attrs, remap.raw)
		if !ok {
			continue
		}
		if _, exists := attrs.Get(remap.canonical); !exists {
			attrs.PutInt(remap.canonical, count)
		}
		attrs.Remove(remap.raw)
	}
}

// mapTTFT remaps Strands' legacy server-side TTFT key onto the canonical
// client key. The wire carries whole milliseconds; canonical stores fractional
// seconds. The canonical key wins when an emitter already provided it.
func mapTTFT(attrs pcommon.Map) {
	const legacyKey = "gen_ai.server.time_to_first_token"
	value, ok := attrs.Get(legacyKey)
	if !ok {
		return
	}
	if _, exists := attrs.Get("gen_ai.response.time_to_first_chunk"); !exists && value.Type() == pcommon.ValueTypeInt {
		attrs.PutDouble("gen_ai.response.time_to_first_chunk", float64(value.Int())/1000)
	}
	attrs.Remove(legacyKey)
	attrs.Remove(legacyKey)
}

// intAttr coerces an attribute to int64 following the connector-wide
// semantics: ints pass through, doubles truncate, strings parse as integers.
func intAttr(attrs pcommon.Map, key string) (int64, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return 0, false
	}
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return value.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(value.Double()), true
	case pcommon.ValueTypeStr:
		parsed, err := strconv.ParseInt(value.Str(), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

// restrictUsageKeys removes every gen_ai.usage.* attribute outside the
// canonical enumeration so unknown vendor counters never reach output.
func restrictUsageKeys(attrs pcommon.Map) {
	var unknown []string
	attrs.Range(func(key string, _ pcommon.Value) bool {
		if strings.HasPrefix(key, "gen_ai.usage.") && !allowedUsageKeys[key] {
			unknown = append(unknown, key)
		}
		return true
	})
	for _, key := range unknown {
		attrs.Remove(key)
	}
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

var _ connector.Traces = (*genAITraceNormalizer)(nil)
