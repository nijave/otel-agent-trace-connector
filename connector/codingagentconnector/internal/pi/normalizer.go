// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package pi normalizes Pi (@amaster.ai/pi-telemetry) native traces into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change. Every native span
// rebuilds its attribute map from the raw one, so canonical output carries the
// mapped vocabulary and nothing else no matter what the extension adds to its
// wire. Non-native spans in claimed groups are dropped, so canonical output
// carries only coding-agent spans.
package pi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

const (
	scopePrefix  = "@amaster.ai/pi-telemetry"
	sdkNameKey   = "telemetry.sdk.name"
	turnSpanName = "chat-turn"
	generation   = "llm-generation"
)

type piTraceNormalizer struct {
	next            consumer.Traces
	captureIdentity bool
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Pi traces-to-traces edge.
func New(next consumer.Traces, captureIdentity bool) connector.Traces {
	return &piTraceNormalizer{next: next, captureIdentity: captureIdentity}
}

func (*piTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeTraces renames native Pi spans into the canonical vocabulary.
// Canonical output carries only coding-agent spans: non-native spans swept
// into a claimed group by the scope/sdk.name claim are dropped here (the raw
// pipelines preserve the originals).
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
		canonical.FilterResource(rs, n.captureIdentity)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			children := make(map[pcommon.SpanID]bool, spans.Len())
			foreign := make(map[pcommon.SpanID]bool, spans.Len())
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				child, native := normalizePiSpan(span, version)
				if !native {
					foreign[span.SpanID()] = true
					continue
				}
				if child {
					children[span.SpanID()] = true
				}
			}
			spans.RemoveIf(func(s ptrace.Span) bool { return foreign[s.SpanID()] })
			reparentOrphans(spans, children)
		}
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool { return ss.Spans().Len() == 0 })
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

// normalizePiSpan rewrites one span into the canonical vocabulary and reports
// whether the span is native Pi and whether it becomes a canonical child of
// an agent root. Canonical attributes are collected in a fresh map that
// replaces the raw one, so every raw key the wire carries is dropped by
// construction.
func normalizePiSpan(span ptrace.Span, version string) (child, native bool) {
	wire := span.Attributes()
	attrs := pcommon.NewMap()
	putPiCommon(attrs, version)
	if toolName := firstSpanString(span, "toolName"); toolName != "" {
		// Live captures show tool spans named after the bare tool with the
		// identity in attributes, so the attribute is the discriminator.
		originalName := span.Name()
		span.SetName("execute_tool" + optionalNameSuffix(toolName))
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		attrs.PutStr("gen_ai.tool.name", toolName)
		attrs.PutStr("coding_agent.source.event", originalName)
		if callID := firstSpanString(span, "toolCallId"); callID != "" {
			attrs.PutStr("gen_ai.tool.call.id", callID)
		}
		attrs.CopyTo(wire)
		return true, true
	}
	switch name := span.Name(); {
	case name == turnSpanName:
		span.SetName("invoke_agent pi")
		// Turn spans arrive as roots or referencing a parent absent from
		// their batch, so each becomes a canonical agent root with any
		// dangling parent cleared.
		span.SetParentSpanID(pcommon.SpanID{})
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		attrs.PutStr("gen_ai.agent.name", "pi")
		attrs.PutStr("coding_agent.source.event", turnSpanName)
		if sessionID := firstSpanString(span, "sessionId"); sessionID != "" {
			attrs.PutStr("gen_ai.conversation.id", sessionID)
		}
	case strings.HasPrefix(name, generation):
		child = true
		// requestedModel is the model the caller asked for; model is what the
		// gateway records, prefixed with the provider. The canonical request
		// model is the requested one, and only an exact
		// "<prefix>/<requestedModel>" pair reveals a provider.
		requestedModel := firstSpanString(span, "requestedModel")
		model := firstSpanString(span, "model")
		requestModel := requestedModel
		if requestModel == "" {
			requestModel = model
		}
		span.SetName("chat" + optionalNameSuffix(requestModel))
		attrs.PutStr("gen_ai.operation.name", "chat")
		attrs.PutStr("coding_agent.source.event", generation)
		if requestModel != "" {
			attrs.PutStr("gen_ai.request.model", requestModel)
		}
		if provider := firstSpanString(span, "provider"); provider != "" {
			attrs.PutStr("gen_ai.provider.name", provider)
		} else if provider := modelProviderPrefix(model, requestedModel); provider != "" {
			attrs.PutStr("gen_ai.provider.name", provider)
		}
		if level := firstSpanString(span, "thinkingLevel"); level != "" {
			attrs.PutStr("gen_ai.request.reasoning.level", level)
		}
		if stop := firstSpanString(span, "stopReason"); stop != "" {
			reasons := attrs.PutEmptySlice("gen_ai.response.finish_reasons")
			reasons.AppendEmpty().SetStr(stop)
		}
		if responseID := firstSpanString(span, "responseId"); responseID != "" {
			attrs.PutStr("gen_ai.response.id", responseID)
		}
		mapUsage(attrs, wire)
	default:
		return false, false
	}
	attrs.CopyTo(wire)
	return child, true
}

// reparentOrphans repairs hierarchy inside a batch: canonical children
// reference the chat-turn span they ran under, and when a batch arrives
// without it they would dangle. Children attach to the slice's first
// invoke_agent span; when the batch carries none, children keep their
// original parent so backends can reattach them once the chat-turn arrives
// (children can land in a batch of their own, mirroring the Claude Code
// export behavior).
func reparentOrphans(spans ptrace.SpanSlice, children map[pcommon.SpanID]bool) {
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
		if !children[span.SpanID()] || exported[span.ParentSpanID()] {
			continue
		}
		if foundRoot && span.ParentSpanID() != agentRoot {
			span.SetParentSpanID(agentRoot)
		}
	}
}

func putPiCommon(attrs pcommon.Map, version string) {
	attrs.PutStr("coding_agent.client.name", "pi")
	attrs.PutStr("coding_agent.source", "native")
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
}

// modelProviderPrefix derives the provider a gateway encodes in front of the
// requested model id ("<prefix>/<requestedModel>"). Anything else yields no
// provider: guessing one from an arbitrary model id would publish a wrong
// gen_ai.provider.name.
func modelProviderPrefix(model, requestedModel string) string {
	if model == "" || requestedModel == "" {
		return ""
	}
	prefix, rest, found := strings.Cut(model, "/")
	if !found || prefix == "" || rest != requestedModel {
		return ""
	}
	return prefix
}

// usageCounters pairs each token counter with its canonical key. The wire
// carries counters either inside the serialized `usage` object (pi-telemetry
// 0.1.28 and later) or as flat `usage.*` attributes (older releases), so both
// sources map. cost and cacheWrite1h have no canonical counterpart.
var usageCounters = []struct {
	field        string // member of the serialized usage object
	flatKey      string // older wire's standalone attribute, empty when none
	canonicalKey string
}{
	{"input", "usage.input", "gen_ai.usage.input_tokens"},
	{"output", "usage.output", "gen_ai.usage.output_tokens"},
	{"totalTokens", "usage.total_tokens", "gen_ai.usage.total_tokens"},
	{"cacheRead", "usage.cache_read", "gen_ai.usage.cache_read.input_tokens"},
	{"cacheWrite", "usage.cache_write", "gen_ai.usage.cache_write.input_tokens"},
	{"reasoning", "", "gen_ai.usage.reasoning.output_tokens"},
}

// mapUsage writes the canonical token counts, preferring the serialized usage
// object per field and falling back to the flat source key when the object
// omits it. A zero count is mapped: the wire reports no cache as 0 rather than
// by omission.
func mapUsage(attrs, wire pcommon.Map) {
	blob := usageBlob(wire)
	for _, counter := range usageCounters {
		if count, ok := blobCount(blob, counter.field); ok {
			attrs.PutInt(counter.canonicalKey, count)
			continue
		}
		if counter.flatKey == "" {
			continue
		}
		if value, ok := wire.Get(counter.flatKey); ok {
			if count, ok := coerceInt(value); ok {
				attrs.PutInt(counter.canonicalKey, count)
			}
		}
	}
}

// usageBlob parses the serialized usage object. A missing or malformed
// attribute yields no members, so the flat keys supply every counter.
func usageBlob(wire pcommon.Map) map[string]any {
	value, ok := wire.Get("usage")
	if !ok || value.Type() != pcommon.ValueTypeStr {
		return nil
	}
	var members map[string]any
	if err := json.Unmarshal([]byte(value.Str()), &members); err != nil {
		return nil
	}
	return members
}

// blobCount coerces one usage member to a token count: OTLP JSON numbers
// decode as float64 and some paths deliver numbers as strings.
func blobCount(blob map[string]any, field string) (int64, bool) {
	switch value := blob[field].(type) {
	case float64:
		return int64(value), true
	case string:
		count, err := strconv.ParseInt(value, 10, 64)
		return count, err == nil
	default:
		return 0, false
	}
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
