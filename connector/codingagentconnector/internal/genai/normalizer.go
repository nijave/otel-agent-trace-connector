// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package genai normalizes GenAI-semantic-convention native traces (the
// opentelemetry-instrumentation-openai-v2 package in both semconv modes,
// direct opentelemetry-util-genai users, and Strands Agents SDK) into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change, and
// content-bearing attributes and events never reach canonical output.
package genai

import (
	"context"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
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
		// Claude groups belong to the Claude normalizer even when they also
		// carry GenAI scopes; claiming here would emit the group twice.
		if claude.ContainsClaudeSpans(inputResourceSpans) {
			continue
		}
		if !containsGenAIScopes(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.CopyTo(rs)
		serviceName := resourceString(rs.Resource(), "service.name")
		serviceVersion := resourceString(rs.Resource(), "service.version")
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j)
			matched := matchesGenAIScope(ss.Scope().Name())
			if !matched {
				continue
			}
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				normalizeSpan(spans.At(k), ss.Scope().Name(), serviceName, serviceVersion)
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
	attrs.PutStr("telemetry.source", "native")
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
	legacyValue, ok := attrs.Get(legacyKey)
	if !ok {
		return
	}
	if _, exists := attrs.Get(currentKey); !exists && legacyValue.Type() == pcommon.ValueTypeInt {
		count := legacyValue.Int()
		attrs.PutInt(currentKey, count)
	}
	attrs.Remove(legacyKey)
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

var _ connector.Traces = (*genAITraceNormalizer)(nil)
