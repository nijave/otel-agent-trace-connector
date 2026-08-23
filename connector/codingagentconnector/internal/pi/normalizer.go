// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package pi normalizes Pi (@amaster.ai/pi-telemetry) native traces into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change, and the raw
// usage blob, Langfuse observation baggage, and flat usage source keys never
// reach canonical output.
package pi

import (
	"context"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	scopePrefix  = "@amaster.ai/pi-telemetry"
	sdkNameKey   = "telemetry.sdk.name"
	turnSpanName = "chat-turn"
	generation   = "llm-generation"
)

type piTraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Pi traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &piTraceNormalizer{next: next}
}

func (*piTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *piTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		if !ContainsPiSpans(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.CopyTo(rs)
		version := resourceString(rs.Resource(), "service.version")
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				normalizePiSpan(spans.At(k), version)
			}
			reparentOrphans(spans)
		}
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsPiSpans reports whether the group carries Pi telemetry, matched by
// instrumentation-scope name or the resource's telemetry.sdk.name. Both are
// set by @amaster.ai/pi-telemetry on every export.
func ContainsPiSpans(resourceSpans ptrace.ResourceSpans) bool {
	if value := resourceString(resourceSpans.Resource(), sdkNameKey); strings.HasPrefix(value, scopePrefix) {
		return true
	}
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		if strings.HasPrefix(resourceSpans.ScopeSpans().At(i).Scope().Name(), scopePrefix) {
			return true
		}
	}
	return false
}

func normalizePiSpan(span ptrace.Span, version string) {
	if toolName := firstSpanString(span, "toolName"); toolName != "" {
		// Live captures show tool spans named after the bare tool with the
		// identity in attributes, so the attribute is the discriminator.
		originalName := span.Name()
		span.SetName("execute_tool" + optionalNameSuffix(toolName))
		attrs := span.Attributes()
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		attrs.PutStr("gen_ai.tool.name", toolName)
		attrs.PutStr("coding_agent.source.event", originalName)
		if callID := firstSpanString(span, "toolCallId"); callID != "" {
			attrs.PutStr("gen_ai.tool.call.id", callID)
		}
		putPiCommon(attrs, version)
		stripPiBaggage(span)
		return
	}
	switch name := span.Name(); {
	case name == turnSpanName:
		span.SetName("invoke_agent pi")
		// The exporter references a parent span it never sends (observed on
		// the wire), so the canonical agent root is this span with the
		// dangling parent cleared.
		span.SetParentSpanID(pcommon.SpanID{})
		attrs := span.Attributes()
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		attrs.PutStr("gen_ai.agent.name", "pi")
		attrs.PutStr("coding_agent.source.event", turnSpanName)
		if sessionID := firstSpanString(span, "sessionId"); sessionID != "" {
			attrs.PutStr("gen_ai.conversation.id", sessionID)
		}
		putPiCommon(attrs, version)
	case strings.HasPrefix(name, generation):
		model := firstSpanString(span, "model")
		span.SetName("chat" + optionalNameSuffix(model))
		attrs := span.Attributes()
		attrs.PutStr("gen_ai.operation.name", "chat")
		attrs.PutStr("coding_agent.source.event", generation)
		if model != "" {
			attrs.PutStr("gen_ai.request.model", model)
		}
		if provider := firstSpanString(span, "provider"); provider != "" {
			attrs.PutStr("gen_ai.provider.name", provider)
		}
		mapUsageCount(attrs, "usage.input", "gen_ai.usage.input_tokens")
		mapUsageCount(attrs, "usage.output", "gen_ai.usage.output_tokens")
		mapUsageCount(attrs, "usage.total_tokens", "gen_ai.usage.total_tokens")
		mapUsageCount(attrs, "usage.cache_read", "gen_ai.usage.cache_read.input_tokens")
		mapUsageCount(attrs, "usage.cache_write", "gen_ai.usage.cache_creation.input_tokens")
		putPiCommon(attrs, version)
	}
	stripPiBaggage(span)
}

// reparentOrphans fixes the broken hierarchy the exporter sends: every span
// references an internal parent it never exports, so canonical children would
// dangle. Chat and execute_tool spans whose parent is missing from the same
// span slice attach to the slice's first invoke_agent span, or become roots
// when the batch carries none (children can land in a batch of their own,
// mirroring the Claude Code export behavior).
func reparentOrphans(spans ptrace.SpanSlice) {
	exported := make(map[pcommon.SpanID]bool, spans.Len())
	var agentRoot pcommon.SpanID
	foundRoot := false
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		exported[span.SpanID()] = true
		if !foundRoot && span.Name() == "invoke_agent pi" {
			agentRoot = span.SpanID()
			foundRoot = true
		}
	}
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		name := span.Name()
		isChild := strings.HasPrefix(name, "chat ") || strings.HasPrefix(name, "execute_tool ")
		if !isChild || exported[span.ParentSpanID()] {
			continue
		}
		if foundRoot && span.ParentSpanID() != agentRoot {
			span.SetParentSpanID(agentRoot)
		} else if !foundRoot {
			span.SetParentSpanID(pcommon.SpanID{})
		}
	}
}

func putPiCommon(attrs pcommon.Map, version string) {
	attrs.PutStr("coding_agent.client.name", "pi")
	attrs.PutStr("telemetry.source", "native")
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
}

// mapUsageCount copies a usage source attribute onto its canonical key,
// coercing the wire value to an integer, then drops the source key. Values
// arrive as strings through some paths (ClickHouse maps) and numbers through
// others (OTLP JSON), so every representation coerces.
func mapUsageCount(attrs pcommon.Map, sourceKey, canonicalKey string) {
	value, ok := attrs.Get(sourceKey)
	if !ok {
		return
	}
	count, ok := coerceInt(value)
	if ok {
		if _, exists := attrs.Get(canonicalKey); !exists {
			attrs.PutInt(canonicalKey, count)
		}
	}
	attrs.Remove(sourceKey)
}

func coerceInt(value pcommon.Value) (int64, bool) {
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return value.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(value.Double()), true
	case pcommon.ValueTypeStr:
		count, err := strconv.ParseInt(value.Str(), 10, 64)
		return count, err == nil
	default:
		return 0, false
	}
}

// stripPiBaggage removes exporter-local metadata that must not reach
// canonical output: Langfuse observation baggage, the serialized usage object,
// and diagnostic fields with no canonical counterpart.
var stripKeys = []string{
	"usage",
	"usage.cost.total",
	"stopReason",
	"status",
}

func stripPiBaggage(span ptrace.Span) {
	attrs := span.Attributes()
	attrs.RemoveIf(func(key string, _ pcommon.Value) bool {
		return strings.HasPrefix(key, "langfuse.")
	})
	for _, key := range stripKeys {
		attrs.Remove(key)
	}
}

func firstSpanString(span ptrace.Span, keys ...string) string {
	for _, key := range keys {
		if value, ok := span.Attributes().Get(key); ok && value.Str() != "" {
			return value.Str()
		}
	}
	return ""
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

func optionalNameSuffix(value string) string {
	if value == "" {
		return ""
	}
	return " " + value
}

var _ connector.Traces = (*piTraceNormalizer)(nil)
