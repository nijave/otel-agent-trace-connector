// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"crypto/sha256"
	"sort"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	scopeName  = "lmnr.tracer"
	clientName = "openhands"
	agentName  = "openhands"

	wireConversation = "conversation"

	attrSpanType   = "lmnr.span.type"
	attrSessionID  = "lmnr.association.properties.session_id"
	attrMetadata   = "lmnr.association.properties.metadata."
	attrToolCallID = attrMetadata + "tool_call_id"
	attrIsDelegate = attrMetadata + "is_delegate"

	syntheticRootDiscriminator = ":synthetic-root"
)

// usageKeys remap the Laminar/LiteLLM accounting keys onto the canonical
// namespace.
var usageKeys = [][2]string{
	{"gen_ai.usage.input_tokens", "gen_ai.usage.input_tokens"},
	{"gen_ai.usage.output_tokens", "gen_ai.usage.output_tokens"},
	{"llm.usage.total_tokens", "gen_ai.usage.total_tokens"},
	{"gen_ai.usage.cache_read_input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"gen_ai.usage.cache_creation_input_tokens", "gen_ai.usage.cache_creation.input_tokens"},
}

// markerSpanNames are the conversation- and agent-family names only the
// OpenHands SDK emits. Scope lmnr.tracer is shared by every
// Laminar-instrumented application, so claiming needs one of these markers
// (or the delegate flag) before a group belongs to this edge.
var markerSpanNames = map[string]bool{
	"conversation":                true,
	"conversation.send_message":   true,
	"conversation.run":            true,
	"conversation.arun":           true,
	"conversation.ask_agent":      true,
	"conversation.generate_title": true,
	"agent.step":                  true,
	"agent.astep":                 true,
	"acp_agent.step":              true,
	"acp_agent.astep":             true,
}

type role int

const (
	roleDrop role = iota
	roleRoot
	roleChat
	roleTool
)

// openhandsTraceNormalizer rewrites OpenHands SDK spans into the canonical
// vocabulary. It is stateless: mid-conversation exports arrive without the
// long-lived conversation root, so each batch is rewritten as-is and
// backends reassemble by the preserved IDs.
type openhandsTraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless OpenHands native traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &openhandsTraceNormalizer{next: next}
}

func (*openhandsTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *openhandsTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputRS := input.ResourceSpans().At(i)
		if !ContainsOpenHandsSpans(inputRS) {
			continue
		}
		groups, order := collect(inputRS)
		if len(groups) == 0 {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputRS.Resource().CopyTo(rs.Resource())
		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName(scopeName)
		for _, key := range order {
			emitGroup(ss.Spans(), groups[key])
		}
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsOpenHandsSpans reports whether any lmnr.tracer scope in the group
// carries an explicit OpenHands marker, keeping generic
// Laminar-instrumented applications unclaimed.
func ContainsOpenHandsSpans(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		ss := resourceSpans.ScopeSpans().At(i)
		if ss.Scope().Name() != scopeName {
			continue
		}
		for j := 0; j < ss.Spans().Len(); j++ {
			span := ss.Spans().At(j)
			if markerSpanNames[span.Name()] {
				return true
			}
			if firstString(span.Attributes(), attrIsDelegate) == "true" {
				return true
			}
		}
	}
	return false
}

// classify maps a wire span to its canonical role via the Laminar span
// type, falling back to the conversation-name check for roots.
func classify(span ptrace.Span) role {
	switch firstString(span.Attributes(), attrSpanType) {
	case "LLM":
		return roleChat
	case "TOOL":
		return roleTool
	}
	if span.Name() == wireConversation {
		return roleRoot
	}
	return roleDrop
}

type kept struct {
	span ptrace.Span
	rol  role
}

type traceGroup struct {
	traceID  pcommon.TraceID
	root     *ptrace.Span
	children []kept
	minStart pcommon.Timestamp
	maxEnd   pcommon.Timestamp
}

// window accumulates the time envelope of every lmnr.tracer span in a
// trace ID regardless of role, so dropped structural intermediates still
// widen their claimed group's root bounds.
type window struct {
	minStart pcommon.Timestamp
	maxEnd   pcommon.Timestamp
}

// collect buckets claimed spans by trace ID. Groups form only from kept
// spans but their envelope folds in every lmnr.tracer span of the same
// trace ID. Groups order deterministically by earliest start then trace-ID
// bytes, so shuffled input yields identical output ordering.
func collect(rs ptrace.ResourceSpans) (map[pcommon.TraceID]*traceGroup, []pcommon.TraceID) {
	groups := map[pcommon.TraceID]*traceGroup{}
	windows := map[pcommon.TraceID]*window{}
	for i := 0; i < rs.ScopeSpans().Len(); i++ {
		ss := rs.ScopeSpans().At(i)
		if ss.Scope().Name() != scopeName {
			continue
		}
		for j := 0; j < ss.Spans().Len(); j++ {
			span := ss.Spans().At(j)
			key := span.TraceID()
			w := windows[key]
			if w == nil {
				w = &window{}
				windows[key] = w
			}
			w.minStart = minTime(w.minStart, span.StartTimestamp())
			w.maxEnd = maxTime(w.maxEnd, span.EndTimestamp())
			rol := classify(span)
			if rol == roleDrop {
				continue
			}
			g := groups[key]
			if g == nil {
				g = &traceGroup{traceID: key}
				groups[key] = g
			}
			switch rol {
			case roleRoot:
				if g.root == nil || span.StartTimestamp() < g.root.StartTimestamp() {
					root := span
					g.root = &root
				}
			default:
				g.children = append(g.children, kept{span: span, rol: rol})
			}
		}
	}
	for id, g := range groups {
		g.minStart = windows[id].minStart
		g.maxEnd = windows[id].maxEnd
	}
	order := make([]pcommon.TraceID, 0, len(groups))
	for id := range groups {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		mi, mj := groups[order[i]], groups[order[j]]
		if mi.minStart != mj.minStart {
			return mi.minStart < mj.minStart
		}
		return string(mi.traceID[:]) < string(mj.traceID[:])
	})
	return groups, order
}

// emitGroup writes one canonical trace: the invoke_agent root followed by
// its reparented chat and execute_tool children in deterministic order.
func emitGroup(dst ptrace.SpanSlice, g *traceGroup) {
	root := dst.AppendEmpty()
	if g.root != nil {
		copySpanMetadata(*g.root, root)
	} else {
		root.SetTraceID(g.traceID)
		root.SetKind(ptrace.SpanKindInternal)
	}
	root.SetSpanID(rootSpanID(g))
	root.SetParentSpanID(pcommon.SpanID{})
	root.SetName("invoke_agent " + agentName)
	root.SetStartTimestamp(g.minStart)
	root.SetEndTimestamp(g.maxEnd)
	putRootAttributes(root.Attributes(), g)

	children := append([]kept(nil), g.children...)
	sort.Slice(children, func(i, j int) bool {
		si, sj := children[i].span, children[j].span
		if si.StartTimestamp() != sj.StartTimestamp() {
			return si.StartTimestamp() < sj.StartTimestamp()
		}
		ii, jj := si.SpanID(), sj.SpanID()
		return string(ii[:]) < string(jj[:])
	})
	// Tool calls arrive once as a call and again as its result sharing a
	// tool_call_id. Deduping after the deterministic sort keeps the
	// earliest record regardless of wire arrival order.
	emittedTools := map[string]bool{}
	for _, child := range children {
		if child.rol == roleTool {
			id := firstString(child.span.Attributes(), attrToolCallID)
			if id != "" {
				if emittedTools[id] {
					continue
				}
				emittedTools[id] = true
			}
		}
		span := dst.AppendEmpty()
		copySpanMetadata(child.span, span)
		span.SetParentSpanID(root.SpanID())
		switch child.rol {
		case roleChat:
			normalizeChat(child.span, span)
		case roleTool:
			normalizeTool(child.span, span)
		}
	}
}

// rootSpanID keeps the conversation span's ID when present; fragment groups
// exported before their root ends get a derived stable ID that cannot
// collide with the SDK's random IDs.
func rootSpanID(g *traceGroup) pcommon.SpanID {
	if g.root != nil {
		return g.root.SpanID()
	}
	h := sha256.New()
	_, _ = h.Write(g.traceID[:])
	_, _ = h.Write([]byte(syntheticRootDiscriminator))
	var id pcommon.SpanID
	copy(id[:], h.Sum(nil)[:8])
	return id
}

func putRootAttributes(attrs pcommon.Map, g *traceGroup) {
	// Root attributes read from the conversation span when present.
	// Fragment groups exported before their root ends inherit the
	// conversation-level attributes from their kept children instead —
	// every OpenHands span carries the association properties.
	src := pcommon.Map{}
	if g.root != nil {
		src = (*g.root).Attributes()
	} else {
		src = pcommon.NewMap()
		for _, k := range g.children {
			k.span.Attributes().Range(func(name string, v pcommon.Value) bool {
				if _, exists := src.Get(name); !exists &&
					strings.HasPrefix(name, "lmnr.association.properties.") {
					v.CopyTo(src.PutEmpty(name))
				}
				return true
			})
		}
	}
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", agentName)
	if sid := firstString(src, attrSessionID); sid != "" {
		attrs.PutStr("gen_ai.conversation.id", sid)
	}
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("coding_agent.source.scope", scopeName)
}

func normalizeChat(wire, span ptrace.Span) {
	attrs := span.Attributes()
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("gen_ai.operation.name", "chat")
	wireAttrs := wire.Attributes()
	if systemValue, ok := wireAttrs.Get("gen_ai.system"); ok {
		// Extract before Put: a map write may invalidate held values.
		provider := systemValue.Str()
		if provider != "" {
			attrs.PutStr("gen_ai.provider.name", provider)
		}
	}
	name := "chat"
	if model := firstString(wireAttrs, "gen_ai.request.model"); model != "" {
		attrs.PutStr("gen_ai.request.model", model)
		name += " " + model
	}
	span.SetName(name)
	for _, pair := range usageKeys {
		if v, ok := wire.Attributes().Get(pair[0]); ok && v.Type() == pcommon.ValueTypeInt {
			attrs.PutInt(pair[1], v.Int())
		}
	}
}

func normalizeTool(wire, span ptrace.Span) {
	attrs := span.Attributes()
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("gen_ai.operation.name", "execute_tool")
	tool := wire.Name()
	attrs.PutStr("gen_ai.tool.name", tool)
	span.SetName("execute_tool " + tool)
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

func firstString(attrs pcommon.Map, key string) string {
	value, ok := attrs.Get(key)
	if !ok || value.Type() != pcommon.ValueTypeStr {
		return ""
	}
	return value.Str()
}

func minTime(a, b pcommon.Timestamp) pcommon.Timestamp {
	if a == 0 || b < a {
		return b
	}
	return a
}

func maxTime(a, b pcommon.Timestamp) pcommon.Timestamp {
	if b > a {
		return b
	}
	return a
}

var _ connector.Traces = (*openhandsTraceNormalizer)(nil)
