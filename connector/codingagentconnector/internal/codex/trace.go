// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

const instrumentationScope = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"

// DefaultScopeVersion is used for the emitted instrumentation scope when the
// Collector build info does not carry a version (for example in unit tests).
const DefaultScopeVersion = "0.1.0"

// tokenUsageAttrs maps Codex completion token counts to their canonical
// destination attributes. It is the single source of truth for per-chat-span
// usage; the invoke_agent root carries no usage of its own.
var tokenUsageAttrs = []struct{ source, dest string }{
	{"input_token_count", "gen_ai.usage.input_tokens"},
	{"output_token_count", "gen_ai.usage.output_tokens"},
	{"cached_token_count", "gen_ai.usage.cache_read.input_tokens"},
	{"tool_token_count", "gen_ai.usage.total_tokens"},
	{"reasoning_token_count", "gen_ai.usage.reasoning.output_tokens"},
}

func buildTrace(turn *turnState, reason, scopeVersion string) (ptrace.Traces, error) {
	events := append([]agentEvent(nil), turn.events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].timestamp.Before(events[j].timestamp) })
	start, end := turnBounds(turn, events)
	traceID := deterministicTraceID(turn, events)
	rootID := deterministicSpanID(traceID, "root")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	resErr := rs.Resource().Attributes().FromRaw(turn.resource)
	canonical.FilterResource(rs)
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
	putRootAttributes(root.Attributes(), turn, events)
	if reason == "timeout" {
		root.Status().SetCode(ptrace.StatusCodeError)
		root.Status().SetMessage("turn finalized after inactivity timeout")
	}

	appendChatSpans(ss.Spans(), traceID, rootID, events, start)
	appendToolSpans(ss.Spans(), traceID, rootID, events)
	appendRootEvents(root.Events(), events)
	if resErr != nil {
		return traces, fmt.Errorf("copy turn resource attributes: %w", resErr)
	}
	return traces, nil
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

func putRootAttributes(attrs pcommon.Map, turn *turnState, events []agentEvent) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "codex")
	attrs.PutStr("gen_ai.provider.name", "openai")
	attrs.PutStr("gen_ai.conversation.id", turn.conversationID)
	attrs.PutStr("coding_agent.client.name", "codex")
	sourceEvent := "codex.user_prompt"
	if !turn.promptSeen && len(events) > 0 {
		sourceEvent = events[0].name
	}
	attrs.PutStr("coding_agent.source.event", sourceEvent)
	attrs.PutStr("coding_agent.source", "normalized")
	if model := lastStringAttr(events, "model"); model != "" {
		attrs.PutStr("gen_ai.request.model", model)
	}
	if version := lastStringAttr(events, "app.version"); version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
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
		if isTimingOnlyCompletion(event.attrs) {
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
		span.Attributes().PutStr("coding_agent.source", "normalized")
		span.Attributes().PutStr("coding_agent.client.name", "codex")
		span.Attributes().PutStr("coding_agent.source.event", event.name)
		if model != "" {
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
		for _, m := range tokenUsageAttrs {
			copyIntAttr(span.Attributes(), event.attrs, m.source, m.dest)
		}
		if ms, ok := int64Value(event.attrs["ttft_ms"]); ok {
			// Wire units are integer milliseconds; canonical stores seconds.
			span.Attributes().PutDouble("gen_ai.response.time_to_first_chunk", float64(ms)/1000)
		}
		completionIndex++
	}
}

func appendToolSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []agentEvent) {
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
		span.Attributes().PutStr("coding_agent.source", "normalized")
		span.Attributes().PutStr("coding_agent.client.name", "codex")
		span.Attributes().PutStr("coding_agent.source.event", event.name)
		if success, ok := boolValue(event.attrs["success"]); ok && !success {
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage("tool execution failed")
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
	sum := sha256.Sum256([]byte(strings.Join([]string{turn.conversationID, fmt.Sprint(anchor.UnixNano())}, "\x00")))
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
	// A milliseconds value too large for time.Duration would wrap on the
	// multiply and could move a span's start past its end; treat it like any
	// other malformed value and report no duration.
	if !ok || ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
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

// isTimingOnlyCompletion reports whether a response.completed record is Codex's
// duplicate rather than the one describing the model call.
//
// Codex logs response.completed twice per call from two different emission sites:
// the SSE frame handler reports only how long the frame took (duration_ms), while
// turn completion reports time-to-first-token plus whatever token counts the
// provider returned. Both timing fields are measured by Codex itself, so ttft_ms
// identifies the turn-completion record even when the provider reported no usage
// at all -- keying on token counts alone would drop every chat span for providers
// that omit usage from the stream.
func isTimingOnlyCompletion(attrs map[string]any) bool {
	if _, ok := int64Value(attrs["ttft_ms"]); ok {
		return false
	}
	return !hasTokenUsage(attrs)
}

// hasTokenUsage reports whether an event carries any completion token count.
func hasTokenUsage(attrs map[string]any) bool {
	for _, m := range tokenUsageAttrs {
		if _, ok := int64Value(attrs[m.source]); ok {
			return true
		}
	}
	return false
}

func copyIntAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value, ok := int64Value(src[from]); ok {
		dst.PutInt(to, value)
	}
}
