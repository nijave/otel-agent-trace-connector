// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strconv"
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
	{"cache_write_token_count", "gen_ai.usage.cache_write.input_tokens"},
	{"tool_token_count", "gen_ai.usage.total_tokens"},
	{"reasoning_token_count", "gen_ai.usage.reasoning.output_tokens"},
}

func buildTrace(turn *turnState, reason, scopeVersion string, captureIdentity bool) (ptrace.Traces, error) {
	events := append([]agentEvent(nil), turn.events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].timestamp.Before(events[j].timestamp) })
	start, end := turnBounds(turn, events)
	traceID := deterministicTraceID(turn, events)
	rootID := deterministicSpanID(traceID, "root")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	resErr := rs.Resource().Attributes().FromRaw(turn.resource)
	canonical.FilterResource(rs, captureIdentity)
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
	putRootAttributes(root.Attributes(), turn, events, captureIdentity)
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

func putRootAttributes(attrs pcommon.Map, turn *turnState, events []agentEvent, captureIdentity bool) {
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
	// terminal.type carries no identity, so the flag does not control it.
	if terminal := lastStringAttr(events, "terminal.type"); terminal != "" {
		attrs.PutStr("coding_agent.terminal.type", terminal)
	}
	// user id and email are identity, emitted only when capture_identity is on.
	if captureIdentity {
		if id := lastStringAttr(events, "user.account_id"); id != "" {
			attrs.PutStr("coding_agent.user.id", id)
		}
		if email := lastStringAttr(events, "user.email"); email != "" {
			attrs.PutStr("coding_agent.user.email", email)
		}
	}
}

func appendChatSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []agentEvent, turnStart time.Time) {
	apiRequests := make([]agentEvent, 0)
	duplicates := duplicateFrameCompletions(events)
	completionIndex := 0
	for i, event := range events {
		if event.name == "codex.api_request" {
			apiRequests = append(apiRequests, event)
			continue
		}
		if event.name != "codex.sse_event" || stringValue(event.attrs["event.kind"]) != "response.completed" {
			continue
		}
		if _, duplicate := duplicates[i]; duplicate {
			continue
		}
		if !describesModelCall(event.attrs) {
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
		if effort := stringValue(event.attrs["model_reasoning_effort"]); effort != "" {
			span.Attributes().PutStr("gen_ai.request.reasoning.level", effort)
		}
		if tier := stringValue(event.attrs["service_tier"]); tier != "" {
			span.Attributes().PutStr("coding_agent.request.service_tier", tier)
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

// deterministicTraceID derives a turn's trace ID from its conversation and
// prompt anchor. The prompt event's fingerprint joins the hash so two turns
// whose prompts share an identical timestamp still get distinct IDs; because
// the fingerprint is content-derived, re-emitting the same turn reproduces
// the same ID.
func deterministicTraceID(turn *turnState, events []agentEvent) pcommon.TraceID {
	anchor := turn.first
	var promptFingerprint *[sha256.Size]byte
	for i := range events {
		if events[i].name == "codex.user_prompt" {
			anchor = events[i].timestamp
			fingerprint := events[i].fingerprint()
			promptFingerprint = &fingerprint
			break
		}
	}
	h := sha256.New()
	_, _ = h.Write([]byte(turn.conversationID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatInt(anchor.UnixNano(), 10)))
	if promptFingerprint != nil {
		_, _ = h.Write(promptFingerprint[:])
	}
	var id pcommon.TraceID
	copy(id[:], h.Sum(nil))
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

// duplicateFrameCompletions returns the indexes in events of SSE frame records
// that duplicate another record of the same model call.
//
// Codex logs response.completed from two sites. The SSE frame poll reports the
// frame it just read, carrying only the Codex-measured duration_ms, before the
// frame is parsed (codex-api/src/sse/responses.rs). The stream consumer then
// reports turn completion, carrying time-to-first-token and token counts, and it
// does so only when the provider's response.completed carried a usage object
// (core/src/client.rs). Both records come from the same task in that order, so a
// frame record is a duplicate exactly when the next response.completed record in
// the turn reports the call. When the provider reports no usage, Codex logs the
// frame record alone: it is then the model call's only record, so it must keep
// the call's chat span instead of being dropped as a duplicate.
func duplicateFrameCompletions(events []agentEvent) map[int]struct{} {
	duplicates := make(map[int]struct{})
	previous := -1
	for i, event := range events {
		if event.name != "codex.sse_event" || stringValue(event.attrs["event.kind"]) != "response.completed" {
			continue
		}
		if previous >= 0 && isFrameCompletion(events[previous].attrs) && completionReportsCall(event.attrs) {
			duplicates[previous] = struct{}{}
		}
		previous = i
	}
	return duplicates
}

// describesModelCall reports whether a response.completed record describes a
// model call. A failed completion reports only error.message, names no call, and
// stays dropped.
func describesModelCall(attrs map[string]any) bool {
	return completionReportsCall(attrs) || isFrameCompletion(attrs)
}

// isFrameCompletion reports whether a response.completed record is the SSE frame
// poll record: it carries the frame's Codex-measured duration and nothing the
// provider reported.
func isFrameCompletion(attrs map[string]any) bool {
	if completionReportsCall(attrs) {
		return false
	}
	_, ok := int64Value(attrs["duration_ms"])
	return ok
}

// completionReportsCall reports whether a response.completed record came from
// Codex's turn-completion site rather than the SSE frame poll. That site logs
// time-to-first-token plus whatever token counts the provider returned; the
// frame poll logs only how long the frame took.
func completionReportsCall(attrs map[string]any) bool {
	if _, ok := int64Value(attrs["ttft_ms"]); ok {
		return true
	}
	return hasTokenUsage(attrs)
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
