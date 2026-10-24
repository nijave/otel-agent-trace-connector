// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package codextraces normalizes Codex's native tracing spans (exported with
// otel.trace_exporter) into the canonical coding-agent vocabulary. It is
// stateless: hierarchy, IDs, kinds, and status pass through; only names and
// attributes change. Codex exports its whole internal span tree, so the edge
// keeps the turn, the usage-bearing response span, and the tool spans, and
// drops everything else. Every kept span rebuilds its attribute map from
// scratch, so tracing-opentelemetry bookkeeping and codex.* keys never reach
// canonical output. The Codex logs edge (internal/codex) synthesizes the same
// trace from logs; the signals config picks which one an operator consumes.
package codextraces

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
)

const (
	clientName = "codex"
	provider   = "openai"

	spanExec            = "codex.exec"
	dispatchPrefix      = "op.dispatch."
	spanMCPToolCall     = "mcp.tools.call"
	toolResultEventName = "codex.tool_result"
	chatMarkerAttr      = "gen_ai.usage.input_tokens"
	rootPrefix          = "invoke_agent " + clientName
)

// turnSpanNames are the session_task spans Codex opens per turn; each becomes
// an invoke_agent root.
var turnSpanNames = map[string]bool{
	"session_task.turn":    true,
	"session_task.compact": true,
	"session_task.review":  true,
}

// turnUsageKeys map the turn span's per-turn totals onto the canonical usage
// keys. non_cached_input_tokens has no canonical counterpart and drops.
var turnUsageKeys = [][2]string{
	{"codex.turn.token_usage.input_tokens", "gen_ai.usage.input_tokens"},
	{"codex.turn.token_usage.cached_input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"codex.turn.token_usage.cache_write_input_tokens", "gen_ai.usage.cache_write.input_tokens"},
	{"codex.turn.token_usage.output_tokens", "gen_ai.usage.output_tokens"},
	{"codex.turn.token_usage.reasoning_output_tokens", "gen_ai.usage.reasoning.output_tokens"},
	{"codex.turn.token_usage.total_tokens", "gen_ai.usage.total_tokens"},
}

// chatUsageKeys map the response span's per-call counters. Four arrive under
// their canonical names already; the two codex.* spellings remap.
var chatUsageKeys = [][2]string{
	{"gen_ai.usage.input_tokens", "gen_ai.usage.input_tokens"},
	{"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.cache_write.input_tokens"},
	{"gen_ai.usage.output_tokens", "gen_ai.usage.output_tokens"},
	{"codex.usage.reasoning_output_tokens", "gen_ai.usage.reasoning.output_tokens"},
	{"codex.usage.total_tokens", "gen_ai.usage.total_tokens"},
}

type spanKind int

const (
	kindDrop spanKind = iota
	kindRoot
	kindChat
	kindTool
)

type codexTraceNormalizer struct {
	next            consumer.Traces
	captureIdentity bool
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Codex native traces-to-traces edge.
func New(next consumer.Traces, captureIdentity bool) connector.Traces {
	return &codexTraceNormalizer{next: next, captureIdentity: captureIdentity}
}

func (*codexTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// spanKey identifies a span within one batch; span ids are unique only
// within a trace.
type spanKey struct {
	trace pcommon.TraceID
	span  pcommon.SpanID
}

func keyOf(span ptrace.Span) spanKey {
	return spanKey{trace: span.TraceID(), span: span.SpanID()}
}

// ConsumeTraces rewrites each claimed group: kept spans are renamed and
// re-attributed, every other span drops, and kept children whose parent left
// the batch reparent onto the batch's turn root for their trace.
func (n *codexTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	// The turn can export in a different resource group than the response
	// spans it models (one export carries several groups), so the model is
	// looked up across the batch's claimed groups rather than per group.
	model := batchTurnModel(input)
	kinds := make(map[spanKey]spanKind, input.SpanCount())
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputRS := input.ResourceSpans().At(i)
		if !ContainsCodexSpans(inputRS) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputRS.CopyTo(rs)
		version := resourceString(rs.Resource(), "service.version")
		canonical.FilterResource(rs, n.captureIdentity)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				kinds[keyOf(span)] = normalizeSpan(span, version, model)
			}
			spans.RemoveIf(func(s ptrace.Span) bool { return kinds[keyOf(s)] == kindDrop })
		}
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool { return ss.Spans().Len() == 0 })
	}
	if output.SpanCount() == 0 {
		return nil
	}
	reparentOrphans(output, kinds)
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsCodexSpans reports whether the group is a Codex export: a scope
// named after the resource's service.name (Codex's tracer takes the service
// name as its scope) holding a Codex marker. The marker keeps unrelated Rust
// services whose scope happens to equal their service name unclaimed. Codex
// exports a span when it ends and splits one export across resource groups,
// so a group often carries response or tool spans without their turn; the
// codex.* attribute keys and the codex.tool_result event are Codex-specific
// and mark those groups too.
func ContainsCodexSpans(rs ptrace.ResourceSpans) bool {
	service := resourceString(rs.Resource(), "service.name")
	if service == "" {
		return false
	}
	for i := 0; i < rs.ScopeSpans().Len(); i++ {
		ss := rs.ScopeSpans().At(i)
		if ss.Scope().Name() != service {
			continue
		}
		for j := 0; j < ss.Spans().Len(); j++ {
			span := ss.Spans().At(j)
			name := span.Name()
			if turnSpanNames[name] || name == spanExec || strings.HasPrefix(name, dispatchPrefix) {
				return true
			}
			if hasToolResultEvent(span) || hasCodexAttr(span.Attributes()) {
				return true
			}
		}
	}
	return false
}

// batchTurnModel returns the model of the input batch's earliest turn span,
// which the response spans inherit; the response span itself names no model.
// Only claimed groups contribute.
func batchTurnModel(input ptrace.Traces) string {
	model := ""
	var earliest pcommon.Timestamp
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		rs := input.ResourceSpans().At(i)
		if !ContainsCodexSpans(rs) {
			continue
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if !turnSpanNames[span.Name()] {
					continue
				}
				if m := firstString(span.Attributes(), "model"); m != "" && (model == "" || span.StartTimestamp() < earliest) {
					model, earliest = m, span.StartTimestamp()
				}
			}
		}
	}
	return model
}

// normalizeSpan classifies one wire span and, for kept kinds, replaces its
// attributes with the canonical set. Tool and chat handling live in Tasks 3
// and 4; this task keeps the turn only.
func normalizeSpan(span ptrace.Span, version, model string) spanKind {
	wire := span.Attributes()
	attrs := pcommon.NewMap()
	putCommon(attrs, version, span.Name())
	switch {
	case turnSpanNames[span.Name()]:
		span.SetName(rootPrefix)
		span.SetParentSpanID(pcommon.SpanID{})
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		attrs.PutStr("gen_ai.agent.name", clientName)
		attrs.PutStr("gen_ai.provider.name", provider)
		if thread := firstString(wire, "thread.id", "conversation.id"); thread != "" {
			attrs.PutStr("gen_ai.conversation.id", thread)
		}
		if m := firstString(wire, "model"); m != "" {
			attrs.PutStr("gen_ai.request.model", m)
		}
		if effort := firstString(wire, "codex.turn.reasoning_effort"); effort != "" {
			attrs.PutStr("gen_ai.request.reasoning.level", effort)
		}
		copyUsage(attrs, wire, turnUsageKeys)
		attrs.CopyTo(wire)
		return kindRoot
	case hasAttr(wire, chatMarkerAttr):
		// Codex renames the response-handling span to the response event
		// type and records the provider's usage on it when the stream
		// completes; the attribute, not the name, identifies the call.
		span.SetName("chat" + optionalNameSuffix(model))
		attrs.PutStr("gen_ai.operation.name", "chat")
		attrs.PutStr("gen_ai.provider.name", provider)
		if model != "" {
			attrs.PutStr("gen_ai.request.model", model)
		}
		if effort := firstString(wire, "codex.request.reasoning_effort"); effort != "" {
			attrs.PutStr("gen_ai.request.reasoning.level", effort)
		}
		copyUsage(attrs, wire, chatUsageKeys)
		attrs.CopyTo(wire)
		return kindChat
	case span.Name() == spanMCPToolCall:
		tool := firstString(wire, "tool.name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		attrs.PutStr("gen_ai.tool.type", "mcp")
		if tool != "" {
			attrs.PutStr("gen_ai.tool.name", tool)
		}
		if callID := firstString(wire, "tool.call_id"); callID != "" {
			attrs.PutStr("gen_ai.tool.call.id", callID)
		}
		if address := firstString(wire, "server.address"); address != "" {
			attrs.PutStr("server.address", address)
		}
		if port, ok := wire.Get("server.port"); ok {
			if n, ok := coerceInt(port); ok {
				attrs.PutInt("server.port", n)
			}
		}
		attrs.CopyTo(wire)
		return kindTool
	case hasToolResultEvent(span):
		result := toolResultEvent(span)
		tool := firstString(result.Attributes(), "tool_name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		attrs.PutStr("gen_ai.tool.type", "builtin")
		if tool != "" {
			attrs.PutStr("gen_ai.tool.name", tool)
		}
		if callID := firstString(result.Attributes(), "call_id"); callID != "" {
			attrs.PutStr("gen_ai.tool.call.id", callID)
		}
		if success, ok := boolValue(result.Attributes(), "success"); ok && !success {
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage("tool execution failed")
		}
		// The event carries only lengths and ids, but it is not canonical; the
		// span now says everything it said.
		span.Events().RemoveIf(func(ptrace.SpanEvent) bool { return true })
		attrs.CopyTo(wire)
		return kindTool
	default:
		return kindDrop
	}
}

// reparentOrphans attaches kept children whose wire parent dropped or never
// arrived onto the batch's earliest turn root carrying the same trace id.
// Codex exports a span when it ends and splits one export across resource
// groups, so the root is sought across the whole batch, not one group. A
// trace with no root in the batch keeps its parent ids: the connector is
// stateless, so a child that exports before its turn stays parented to the
// dropped wire span and a backend groups it with the turn by trace id.
func reparentOrphans(output ptrace.Traces, kinds map[spanKey]spanKind) {
	roots := map[pcommon.TraceID]ptrace.Span{}
	exported := make(map[spanKey]bool, len(kinds))
	eachSpan(output, func(span ptrace.Span) {
		exported[keyOf(span)] = true
		if kinds[keyOf(span)] != kindRoot {
			return
		}
		if current, ok := roots[span.TraceID()]; !ok || span.StartTimestamp() < current.StartTimestamp() {
			roots[span.TraceID()] = span
		}
	})
	eachSpan(output, func(span ptrace.Span) {
		if kinds[keyOf(span)] == kindRoot || exported[spanKey{trace: span.TraceID(), span: span.ParentSpanID()}] {
			return
		}
		if root, ok := roots[span.TraceID()]; ok {
			span.SetParentSpanID(root.SpanID())
		}
	})
}

func eachSpan(traces ptrace.Traces, visit func(ptrace.Span)) {
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				visit(spans.At(k))
			}
		}
	}
}

func putCommon(attrs pcommon.Map, version, wireName string) {
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("coding_agent.source.event", wireName)
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
}

// copyUsage writes each present counter under its canonical key. tracing
// records integers as int64; a double or numeric string coerces the same way
// the other edges do, and a malformed value maps nothing.
func copyUsage(attrs, wire pcommon.Map, keys [][2]string) {
	for _, pair := range keys {
		if value, ok := wire.Get(pair[0]); ok {
			if n, ok := coerceInt(value); ok {
				attrs.PutInt(pair[1], n)
			}
		}
	}
}

func coerceInt(value pcommon.Value) (int64, bool) {
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return value.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(value.Double()), true
	case pcommon.ValueTypeStr:
		n, err := strconv.ParseInt(value.Str(), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

// isToolResultEvent recognizes Codex's trace-safe tool_result copy. The
// tracing bridge names a span event after the tracing event's name, which
// Codex sets to the event name on some paths and to "event <file>:<line>" on
// others; the event.name attribute is the stable marker.
func isToolResultEvent(event ptrace.SpanEvent) bool {
	return event.Name() == toolResultEventName || firstString(event.Attributes(), "event.name") == toolResultEventName
}

func hasToolResultEvent(span ptrace.Span) bool {
	for i := 0; i < span.Events().Len(); i++ {
		if isToolResultEvent(span.Events().At(i)) {
			return true
		}
	}
	return false
}

// toolResultEvent returns the span's first tool_result event; callers check
// hasToolResultEvent first.
func toolResultEvent(span ptrace.Span) ptrace.SpanEvent {
	for i := 0; i < span.Events().Len(); i++ {
		if isToolResultEvent(span.Events().At(i)) {
			return span.Events().At(i)
		}
	}
	return ptrace.NewSpanEvent()
}

// boolValue reads a bool attribute that tracing may have recorded as a
// string.
func boolValue(attrs pcommon.Map, key string) (bool, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return false, false
	}
	switch value.Type() {
	case pcommon.ValueTypeBool:
		return value.Bool(), true
	case pcommon.ValueTypeStr:
		b, err := strconv.ParseBool(value.Str())
		return b, err == nil
	default:
		return false, false
	}
}

func hasAttr(attrs pcommon.Map, key string) bool {
	_, ok := attrs.Get(key)
	return ok
}

// hasCodexAttr reports whether any attribute key sits in Codex's own
// namespace (codex.turn.*, codex.usage.*, codex.request.*, ...).
func hasCodexAttr(attrs pcommon.Map) bool {
	found := false
	attrs.Range(func(key string, _ pcommon.Value) bool {
		found = strings.HasPrefix(key, "codex.")
		return !found
	})
	return found
}

func optionalNameSuffix(value string) string {
	if value == "" {
		return ""
	}
	return " " + value
}

// firstString returns the first string value for any of the keys. It scans
// the entries instead of using Get because tracing's bookkeeping can put a
// non-string attribute under the same key as a Codex one (the turn span
// carries both the tokio thread's int thread.id and Codex's string
// thread.id), and Get stops at the first match.
func firstString(attrs pcommon.Map, keys ...string) string {
	for _, key := range keys {
		found := ""
		attrs.Range(func(k string, value pcommon.Value) bool {
			if k == key && value.Type() == pcommon.ValueTypeStr && value.Str() != "" {
				found = value.Str()
				return false
			}
			return true
		})
		if found != "" {
			return found
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

var _ connector.Traces = (*codexTraceNormalizer)(nil)
