// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package genai normalizes GenAI-semantic-convention native traces (the
// opentelemetry-instrumentation-openai-v2 package in both semconv modes,
// direct opentelemetry-util-genai users, and Strands Agents SDK) into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change, and canonical
// output is restricted to an allowlist of benign attributes and events, so
// content never reaches it — not even from unknown vendor namespaces.
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
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/opencode"
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
		// Claude, OpenCode, and Pi groups belong to their own normalizers
		// even when they also carry GenAI scopes; claiming here would emit
		// the group twice, and OpenCode's raw ai.* attributes would survive
		// stripContent.
		if claude.ContainsClaudeSpans(inputResourceSpans) {
			continue
		}
		if opencode.ContainsOpenCodeSpans(inputResourceSpans) {
			continue
		}
		if pi.ContainsPiSpans(inputResourceSpans) {
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
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if matched {
					normalizeSpan(span, ss.Scope().Name(), serviceName, serviceVersion)
				}
				stripContent(span)
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

// canonicalAttributeKeys are the only span attributes allowed to reach
// canonical output: identity, provenance, and operational metadata written by
// this normalizer or carried benignly by the claimed scopes. Everything else
// — unknown vendor namespaces, legacy layouts, future semconv keys — fails
// closed instead of riding along.
var canonicalAttributeKeys = map[string]bool{
	// Written by normalizeSpan for every claimed span.
	"telemetry.source":            true,
	"coding_agent.source.scope":   true,
	"coding_agent.client.name":    true,
	"coding_agent.client.version": true,

	// GenAI request/response and agent/tool metadata carried by the fixtures.
	"gen_ai.operation.name":               true,
	"gen_ai.provider.name":                true,
	"gen_ai.request.model":                true,
	"gen_ai.request.max_tokens":           true,
	"gen_ai.request.stream":               true,
	"gen_ai.response.finish_reasons":      true,
	"gen_ai.response.id":                  true,
	"gen_ai.response.model":               true,
	"gen_ai.response.time_to_first_chunk": true,
	"gen_ai.agent.id":                     true,
	"gen_ai.agent.name":                   true,
	"gen_ai.agent.version":                true,
	"gen_ai.conversation.id":              true,
	"gen_ai.tool.call.id":                 true,
	"gen_ai.tool.name":                    true,
	"gen_ai.tool.type":                    true,
	"gen_ai.tool.status":                  true,
	"gen_ai.server.time_to_first_token":   true,
	"gen_ai.event.start_time":             true,
	"gen_ai.event.end_time":               true,

	// Server identity per the GenAI semantic conventions.
	"server.address": true,
	"server.port":    true,

	// Strands event-loop correlation IDs and exception event details.
	"event_loop.cycle_id":        true,
	"event_loop.parent_cycle_id": true,
	"exception.type":             true,
	"exception.message":          true,
	"exception.escaped":          true,
	"exception.stacktrace":       true,

	// Copilot operational signal: billing counters, latency, correlation IDs,
	// hook decisions, and session usage-event metrics.
	"github.copilot.cost":                        true,
	"github.copilot.aiu":                         true,
	"github.copilot.turn_id":                     true,
	"github.copilot.interaction_id":              true,
	"github.copilot.turn_count":                  true,
	"github.copilot.server_duration":             true,
	"github.copilot.hook.decision":               true,
	"github.copilot.token_limit":                 true,
	"github.copilot.current_tokens":              true,
	"github.copilot.messages_length":             true,
	"github.copilot.total_premium_requests":      true,
	"github.copilot.user.message.source":         true,
	"github.copilot.user.message.interaction_id": true,
}

// canonicalAttributePrefixes are safe attribute families too large to
// enumerate: emitters mint new usage counters freely, and every key below
// gen_ai.usage. is a token count.
var canonicalAttributePrefixes = []string{
	"gen_ai.usage.",
}

// contentEventNames are span events removed entirely, attributes included.
var contentEventNames = map[string]bool{
	"gen_ai.client.inference.operation.details": true,
	"gen_ai.user.message":                       true,
	"gen_ai.assistant.message":                  true,
	"gen_ai.system.message":                     true,
	"gen_ai.tool.message":                       true,
	"gen_ai.choice":                             true,
	"memory.query":                              true,
	"memory.content":                            true,
}

func stripContent(span ptrace.Span) {
	stripContentAttributes(span.Attributes())
	span.Events().RemoveIf(func(event ptrace.SpanEvent) bool {
		if contentEventNames[event.Name()] {
			return true
		}
		stripContentAttributes(event.Attributes())
		return false
	})
}

// stripContentAttributes reduces an attribute map to the canonical allowlist.
func stripContentAttributes(attributes pcommon.Map) {
	attributes.RemoveIf(func(key string, _ pcommon.Value) bool {
		return !isCanonicalAttribute(key)
	})
}

func isCanonicalAttribute(key string) bool {
	if canonicalAttributeKeys[key] {
		return true
	}
	for _, prefix := range canonicalAttributePrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

var _ connector.Traces = (*genAITraceNormalizer)(nil)
