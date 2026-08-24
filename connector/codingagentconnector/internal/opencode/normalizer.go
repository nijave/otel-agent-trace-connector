// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

const (
	scopeName  = "opencode"
	clientName = "opencode"
	agentName  = "opencode"

	wireStreamText = "ai.streamText"
	wireDoStream   = "ai.streamText.doStream"
	wireToolCall   = "ai.toolCall"
)

// opencodeTraceNormalizer rewrites OpenCode's native Vercel AI SDK spans into
// the canonical vocabulary and drops everything else OpenCode exports. It is
// stateless: children can land in exports without their ancestors, so each
// batch is rewritten as-is and backends reassemble by the preserved IDs.
type opencodeTraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless OpenCode native traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &opencodeTraceNormalizer{next: next}
}

func (*opencodeTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *opencodeTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		if !ContainsOpenCodeSpans(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.Resource().CopyTo(rs.Resource())
		version := resourceString(rs.Resource(), "service.version")
		// Read raw keys before the filter: session.id feeds conversation ids
		// but is not part of the canonical resource vocabulary.
		resourceSessionID := resourceString(rs.Resource(), "session.id")
		canonical.FilterResource(rs)
		for j := 0; j < inputResourceSpans.ScopeSpans().Len(); j++ {
			inputScopeSpans := inputResourceSpans.ScopeSpans().At(j)
			ss := rs.ScopeSpans().AppendEmpty()
			inputScopeSpans.Scope().CopyTo(ss.Scope())
			spans := ss.Spans()
			for k := 0; k < inputScopeSpans.Spans().Len(); k++ {
				wire := inputScopeSpans.Spans().At(k)
				if !isClaimedSpan(wire.Name()) {
					continue
				}
				span := spans.AppendEmpty()
				copySpanMetadata(wire, span)
				normalizeSpan(wire, span, version, resourceSessionID)
			}
		}
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool { return ss.Spans().Len() == 0 })
	}
	output.ResourceSpans().RemoveIf(func(rs ptrace.ResourceSpans) bool { return rs.ScopeSpans().Len() == 0 })
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsOpenCodeSpans reports whether any scope in the group is OpenCode's
// native tracer. Exact match: prefixed scopes such as opencode.* plugins or
// Kilo's com.opencode are separate surfaces this edge must not claim.
func ContainsOpenCodeSpans(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		if resourceSpans.ScopeSpans().At(i).Scope().Name() == scopeName {
			return true
		}
	}
	return false
}

// isClaimedSpan lists the exact wire span names rewritten into canonical
// vocabulary; every other span OpenCode exports is internal Effect
// instrumentation and never enters canonical output.
func isClaimedSpan(name string) bool {
	switch name {
	case wireStreamText, wireDoStream, wireToolCall:
		return true
	}
	return false
}

func normalizeSpan(wire, span ptrace.Span, version, resourceSessionID string) {
	attrs := span.Attributes()
	putCommon(attrs, version)
	copyWireStrings(wire.Attributes(), attrs)
	attrs.PutStr("coding_agent.source.event", wire.Name())
	switch wire.Name() {
	case wireStreamText:
		// The wire nests each step under internal Effect spans
		// (SessionProcessor.process -> LLM.run) that never reach canonical
		// output, so a kept parent would dangle downstream; this renamed span
		// is a true root.
		span.SetParentSpanID(pcommon.SpanID{})
		sessionID := firstString(wire.Attributes(), "session.id")
		if sessionID == "" {
			sessionID = resourceSessionID
		}
		if sessionID != "" {
			attrs.PutStr("gen_ai.conversation.id", sessionID)
		}
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		attrs.PutStr("gen_ai.agent.name", agentName)
		copyUsage(wire.Attributes(), attrs)
		span.SetName("invoke_agent " + agentName)
	case wireDoStream:
		model := firstString(wire.Attributes(), "gen_ai.request.model")
		name := "chat"
		if model != "" {
			attrs.PutStr("gen_ai.request.model", model)
			name += " " + model
		}
		attrs.PutStr("gen_ai.operation.name", "chat")
		copyUsage(wire.Attributes(), attrs)
		passthroughCanonicalUsage(wire.Attributes(), attrs)
		if total, ok := intValue(wire.Attributes(), "ai.usage.totalTokens"); ok {
			attrs.PutInt("gen_ai.usage.total_tokens", total)
		}
		reasoning, ok := intValue(wire.Attributes(), "ai.usage.reasoningTokens")
		if !ok {
			reasoning, ok = intValue(wire.Attributes(), "ai.usage.outputTokenDetails.reasoningTokens")
		}
		if ok {
			attrs.PutInt("gen_ai.usage.reasoning.output_tokens", reasoning)
		}
		if ttft, ok := intValue(wire.Attributes(), "ai.response.msToFirstChunk"); ok {
			// Wire units are fractional milliseconds; canonical stores whole
			// milliseconds like every other edge.
			attrs.PutInt("gen_ai.response.time_to_first_chunk", ttft)
		}
		span.SetName(name)
	case wireToolCall:
		tool := firstString(wire.Attributes(), "ai.toolCall.name")
		name := "execute_tool"
		if tool != "" {
			attrs.PutStr("gen_ai.tool.name", tool)
			name += " " + tool
		}
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		span.SetName(name)
	}
}

func putCommon(attrs pcommon.Map, version string) {
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
}

var usageKeys = [][2]string{
	{"ai.usage.inputTokens", "gen_ai.usage.input_tokens"},
	{"ai.usage.outputTokens", "gen_ai.usage.output_tokens"},
	{"ai.usage.cachedInputTokens", "gen_ai.usage.cache_read.input_tokens"},
	{"ai.usage.totalTokens", "gen_ai.usage.total_tokens"},
}

// wireStringAttrs maps OpenCode string attributes onto canonical destinations,
// the string counterpart to the token usage table above.
var wireStringAttrs = [][2]string{
	{"ai.model.provider", "gen_ai.provider.name"},
}

func copyWireStrings(from, to pcommon.Map) {
	for _, pair := range wireStringAttrs {
		if value := firstString(from, pair[0]); value != "" {
			to.PutStr(pair[1], value)
		}
	}
}

func copyUsage(from, to pcommon.Map) {
	for _, pair := range usageKeys {
		if value, ok := from.Get(pair[0]); ok && value.Type() == pcommon.ValueTypeInt {
			to.PutInt(pair[1], value.Int())
		}
	}
}

// passthroughCanonicalUsage keeps the ready-made canonical usage counters the
// wire already carries under their canonical names; the ai.usage.* mappings
// above land on the same slots with the same values.
func passthroughCanonicalUsage(from, to pcommon.Map) {
	for _, key := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens"} {
		if value, ok := intValue(from, key); ok {
			to.PutInt(key, value)
		}
	}
}

// intValue reads an attribute as an int64: ints pass through and doubles
// truncate, matching the coercion semantics used across the other edges.
func intValue(attrs pcommon.Map, key string) (int64, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return 0, false
	}
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return value.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(value.Double()), true
	}
	return 0, false
}

func firstString(attrs pcommon.Map, key string) string {
	value, ok := attrs.Get(key)
	if !ok || value.Str() == "" {
		return ""
	}
	return value.Str()
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

func copySpanMetadata(wire, span ptrace.Span) {
	span.SetTraceID(wire.TraceID())
	span.SetSpanID(wire.SpanID())
	span.SetParentSpanID(wire.ParentSpanID())
	span.SetKind(wire.Kind())
	span.SetStartTimestamp(wire.StartTimestamp())
	span.SetEndTimestamp(wire.EndTimestamp())
	span.SetFlags(wire.Flags())
	span.SetDroppedAttributesCount(wire.DroppedAttributesCount())
	status := wire.Status()
	span.Status().SetCode(status.Code())
	span.Status().SetMessage(status.Message())
}

var _ connector.Traces = (*opencodeTraceNormalizer)(nil)
