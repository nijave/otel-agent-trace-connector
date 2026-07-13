// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const instrumentationScope = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"

// defaultScopeVersion is used for the emitted instrumentation scope when the
// Collector build info does not carry a version (for example in unit tests).
const defaultScopeVersion = "0.1.0"

func buildTrace(turn *turnState, reason, scopeVersion string) ptrace.Traces {
	events := append([]agentEvent(nil), turn.events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].timestamp.Before(events[j].timestamp) })
	start, end := turnBounds(turn, events)
	traceID := deterministicTraceID(turn, events)
	rootID := deterministicSpanID(traceID, "root")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	_ = rs.Resource().Attributes().FromRaw(turn.resource)
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(instrumentationScope)
	ss.Scope().SetVersion(scopeVersion)

	root := ss.Spans().AppendEmpty()
	root.SetName("invoke_agent codex")
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	root.SetKind(ptrace.SpanKindInternal)
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
	putRootAttributes(root.Attributes(), turn, events, reason)
	if reason == "timeout" {
		root.Status().SetCode(ptrace.StatusCodeError)
		root.Status().SetMessage("turn finalized after inactivity timeout")
	}

	appendChatSpans(ss.Spans(), traceID, rootID, events, start)
	appendToolSpans(ss.Spans(), traceID, rootID, events)
	appendRootEvents(root.Events(), events)
	return traces
}

func turnBounds(turn *turnState, events []agentEvent) (time.Time, time.Time) {
	start, end := turn.first, turn.last
	for _, event := range events {
		if duration := durationFromAttrs(event.attrs); duration > 0 {
			candidate := event.timestamp.Add(-duration)
			if candidate.Before(start) {
				start = candidate
			}
		}
		if event.timestamp.Before(start) {
			start = event.timestamp
		}
		if event.timestamp.After(end) {
			end = event.timestamp
		}
	}
	if end.Before(start) {
		end = start
	}
	return start, end
}

func putRootAttributes(attrs pcommon.Map, turn *turnState, events []agentEvent, reason string) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "codex")
	attrs.PutStr("gen_ai.provider.name", "openai")
	attrs.PutStr("gen_ai.conversation.id", turn.key.conversationID)
	attrs.PutStr("coding_agent.client.name", "codex")
	sourceEvent := "codex.user_prompt"
	if !turn.promptSeen && len(events) > 0 {
		sourceEvent = events[0].name
	}
	attrs.PutStr("coding_agent.source.event", sourceEvent)
	attrs.PutStr("coding_agent.turn.finish_reason", reason)
	attrs.PutBool("coding_agent.turn.complete", reason == "completed")
	attrs.PutBool("coding_agent.turn.prompt_observed", turn.promptSeen)
	attrs.PutBool("coding_agent.turn.events_truncated", turn.truncated)
	attrs.PutStr("telemetry.source", "normalized")
	if model := lastStringAttr(events, "model"); model != "" {
		attrs.PutStr("gen_ai.request.model", model)
	}
	if version := lastStringAttr(events, "app.version"); version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
	putAggregateUsage(attrs, events)
}

func appendChatSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []agentEvent, turnStart time.Time) {
	apiRequests := make([]agentEvent, 0)
	completionIndex := 0
	for _, event := range events {
		if event.name == "codex.api_request" {
			apiRequests = append(apiRequests, event)
			continue
		}
		if event.name != "codex.sse_event" || stringValue(event.attrs["event.kind"]) != "response.completed" {
			continue
		}
		start := turnStart
		if len(apiRequests) > 0 {
			request := apiRequests[len(apiRequests)-1]
			start = request.timestamp.Add(-durationFromAttrs(request.attrs))
			// Earlier requests are retries for this completion, not starts for a
			// later model call.
			apiRequests = apiRequests[:0]
		}
		if start.After(event.timestamp) {
			start = event.timestamp
		}
		span := spans.AppendEmpty()
		model := stringValue(event.attrs["model"])
		name := "chat"
		if model != "" {
			name += " " + model
		}
		span.SetName(name)
		span.SetTraceID(traceID)
		span.SetSpanID(deterministicSpanID(traceID, fmt.Sprintf("chat:%d:%d", completionIndex, event.timestamp.UnixNano())))
		span.SetParentSpanID(parentID)
		span.SetKind(ptrace.SpanKindClient)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(event.timestamp))
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("gen_ai.provider.name", "openai")
		span.Attributes().PutStr("coding_agent.source.event", event.name)
		if model != "" {
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
		copyIntAttr(span.Attributes(), event.attrs, "input_token_count", "gen_ai.usage.input_tokens")
		copyIntAttr(span.Attributes(), event.attrs, "output_token_count", "gen_ai.usage.output_tokens")
		copyIntAttr(span.Attributes(), event.attrs, "cached_token_count", "gen_ai.usage.cache_read.input_tokens")
		copyIntAttr(span.Attributes(), event.attrs, "reasoning_token_count", "coding_agent.usage.reasoning_tokens")
		completionIndex++
	}
}

func appendToolSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []agentEvent) {
	decisions := make(map[string][]agentEvent)
	for _, event := range events {
		if event.name == "codex.tool_decision" {
			callID := stringValue(event.attrs["call_id"])
			if callID != "" {
				decisions[callID] = append(decisions[callID], event)
			}
		}
	}
	toolIndex := 0
	for _, event := range events {
		if event.name != "codex.tool_result" {
			continue
		}
		tool := stringValue(event.attrs["tool_name"])
		if tool == "" {
			tool = "unknown"
		}
		callID := stringValue(event.attrs["call_id"])
		start := event.timestamp.Add(-durationFromAttrs(event.attrs))
		span := spans.AppendEmpty()
		span.SetName("execute_tool " + tool)
		span.SetTraceID(traceID)
		span.SetSpanID(deterministicSpanID(traceID, fmt.Sprintf("tool:%s:%d:%d", callID, toolIndex, event.timestamp.UnixNano())))
		span.SetParentSpanID(parentID)
		span.SetKind(ptrace.SpanKindInternal)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(event.timestamp))
		span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
		span.Attributes().PutStr("gen_ai.tool.name", tool)
		span.Attributes().PutStr("coding_agent.source.event", event.name)
		if callID != "" {
			span.Attributes().PutStr("coding_agent.tool.call_id", callID)
		}
		if success, ok := boolValue(event.attrs["success"]); ok {
			span.Attributes().PutBool("coding_agent.tool.success", success)
			if !success {
				span.Status().SetCode(ptrace.StatusCodeError)
				span.Status().SetMessage("tool execution failed")
			}
		}
		for _, decision := range decisions[callID] {
			e := span.Events().AppendEmpty()
			e.SetName("codex.tool_decision")
			e.SetTimestamp(pcommon.NewTimestampFromTime(decision.timestamp))
			copyStringAttr(e.Attributes(), decision.attrs, "decision", "coding_agent.tool.decision")
			copyStringAttr(e.Attributes(), decision.attrs, "source", "coding_agent.tool.decision_source")
		}
		toolIndex++
	}
}

func appendRootEvents(dst ptrace.SpanEventSlice, events []agentEvent) {
	for _, event := range events {
		if event.name == "codex.user_prompt" || event.name == "codex.tool_decision" || event.name == "codex.tool_result" ||
			(event.name == "codex.sse_event" && stringValue(event.attrs["event.kind"]) == "response.completed") {
			continue
		}
		e := dst.AppendEmpty()
		e.SetName(event.name)
		e.SetTimestamp(pcommon.NewTimestampFromTime(event.timestamp))
		copyStringAttr(e.Attributes(), event.attrs, "event.kind", "coding_agent.source.event_kind")
		copyStringAttr(e.Attributes(), event.attrs, "error.message", "error.message")
	}
}

func deterministicTraceID(turn *turnState, events []agentEvent) pcommon.TraceID {
	anchor := turn.first
	for _, event := range events {
		if event.name == "codex.user_prompt" {
			anchor = event.timestamp
			break
		}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{turn.key.provider, turn.key.conversationID, fmt.Sprint(anchor.UnixNano())}, "\x00")))
	var id pcommon.TraceID
	copy(id[:], sum[:16])
	return id
}

func deterministicSpanID(traceID pcommon.TraceID, discriminator string) pcommon.SpanID {
	h := sha256.New()
	_, _ = h.Write(traceID[:])
	_, _ = h.Write([]byte(discriminator))
	sum := h.Sum(nil)
	var id pcommon.SpanID
	copy(id[:], sum[:8])
	return id
}

func durationFromAttrs(attrs map[string]any) time.Duration {
	ms, ok := int64Value(attrs["duration_ms"])
	if !ok || ms < 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func lastStringAttr(events []agentEvent, key string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if value := stringValue(events[i].attrs[key]); value != "" {
			return value
		}
	}
	return ""
}

func putAggregateUsage(attrs pcommon.Map, events []agentEvent) {
	totals := map[string]int64{}
	seen := map[string]bool{}
	for _, event := range events {
		if event.name != "codex.sse_event" || stringValue(event.attrs["event.kind"]) != "response.completed" {
			continue
		}
		for _, key := range []string{"input_token_count", "output_token_count", "cached_token_count", "reasoning_token_count"} {
			if value, ok := int64Value(event.attrs[key]); ok {
				totals[key] += value
				seen[key] = true
			}
		}
	}
	mappings := map[string]string{
		"input_token_count":     "gen_ai.usage.input_tokens",
		"output_token_count":    "gen_ai.usage.output_tokens",
		"cached_token_count":    "gen_ai.usage.cache_read.input_tokens",
		"reasoning_token_count": "coding_agent.usage.reasoning_tokens",
	}
	for source, destination := range mappings {
		if seen[source] {
			attrs.PutInt(destination, totals[source])
		}
	}
}

func copyIntAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value, ok := int64Value(src[from]); ok {
		dst.PutInt(to, value)
	}
}

func copyStringAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value := stringValue(src[from]); value != "" {
		dst.PutStr(to, value)
	}
}
