// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"crypto/sha256"
	"sort"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const instrumentationScope = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"

// chatTokenAttrs maps the api_request token ints to their canonical
// destinations; it drives both per-span usage and the root rollup.
var chatTokenAttrs = []struct{ source, dest string }{
	{"cursor.api.request.input_tokens", "gen_ai.usage.input_tokens"},
	{"cursor.api.request.output_tokens", "gen_ai.usage.output_tokens"},
	{"cursor.api.request.cache_read_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"cursor.api.request.cache_creation_tokens", "gen_ai.usage.cache_creation.input_tokens"},
}

type attrMapping struct{ source, dest string }

var rootEventAllowlist = map[string][]attrMapping{
	BodySkillActivated: {
		{"cursor.skill.name", "coding_agent.cursor.skill.name"},
		{"cursor.skill.trigger", "coding_agent.cursor.skill.trigger"},
		{"cursor.skill.source", "coding_agent.cursor.skill.source"},
		{"cursor.plugin.name", "coding_agent.cursor.plugin.name"},
	},
	BodyHookExecutionComplete: {
		{"cursor.hook.name", "coding_agent.cursor.hook.name"},
		{"cursor.hook.type", "coding_agent.cursor.hook.type"},
		{"cursor.hook.outcome", "coding_agent.cursor.hook.outcome"},
		{"cursor.hook.duration_ms", "coding_agent.cursor.hook.duration_ms"},
		{"cursor.plugin.name", "coding_agent.cursor.plugin.name"},
	},
}

// cloudAgentAllowlist covers the cloud_agents family; bodies are prefixed so
// one table serves all of them.
var cloudAgentAllowlist = []attrMapping{
	{"cursor.cloud_agent.pull_request.kind", "coding_agent.cursor.pull_request.kind"},
	{"cursor.cloud_agent.pull_request.number", "coding_agent.cursor.pull_request.number"},
	{"cursor.cloud_agent.pull_request.draft", "coding_agent.cursor.pull_request.draft"},
	{"cursor.cloud_agent.setup.kind", "coding_agent.cursor.setup.kind"},
	{"cursor.cloud_agent.setup.duration_ms", "coding_agent.cursor.setup.duration_ms"},
	{"cursor.cloud_agent.setup.reason", "coding_agent.cursor.setup.reason"},
	{"cursor.cloud_agent.artifact.file_name", "coding_agent.cursor.artifact.file_name"},
	{"cursor.cloud_agent.artifact.content_type", "coding_agent.cursor.artifact.content_type"},
	{"cursor.mcp.server.name", "coding_agent.cursor.mcp.server.name"},
}

func buildTrace(burst *burstState, reason, scopeVersion string) ptrace.Traces {
	events := append([]Event(nil), burst.events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	traceID := deterministicTraceID(burst.conversationID, events)
	rootID := deterministicSpanID(traceID, "root")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	_ = rs.Resource().Attributes().FromRaw(burst.resource)
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(instrumentationScope)
	ss.Scope().SetVersion(scopeVersion)

	root := ss.Spans().AppendEmpty()
	root.SetName("invoke_agent cursor")
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	root.SetKind(ptrace.SpanKindInternal)
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(burst.first))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(burst.last))
	putRootAttributes(root.Attributes(), burst, events, reason)
	if reason == "timeout" {
		root.Status().SetCode(ptrace.StatusCodeError)
		root.Status().SetMessage("burst closed after turn_timeout")
	}

	appendChatSpans(ss.Spans(), traceID, rootID, events)
	appendRootEvents(root.Events(), events)
	return traces
}

func putRootAttributes(attrs pcommon.Map, burst *burstState, events []Event, reason string) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "cursor")
	attrs.PutStr("gen_ai.conversation.id", burst.conversationID)
	attrs.PutStr("coding_agent.client.name", "cursor")
	if len(events) > 0 {
		attrs.PutStr("coding_agent.source.event", events[0].Body)
	}
	attrs.PutStr("coding_agent.turn.finish_reason", reason)
	attrs.PutBool("coding_agent.turn.events_truncated", burst.truncated)
	attrs.PutStr("telemetry.source", "normalized")
	// Deliberately no gen_ai.provider.name: the wire never names the upstream
	// provider and the connector does not guess one. Deliberately no
	// coding_agent.turn.complete: quiet closing cannot distinguish a finished
	// model turn from an abandoned one.
	copyStringAttr(attrs, burst.resource, "service.version", "coding_agent.client.version")
	copyStringAttr(attrs, burst.resource, "cursor.surface", "coding_agent.cursor.surface")
	copyStringAttr(attrs, burst.resource, "cursor.entrypoint", "coding_agent.cursor.entrypoint")
	copyIntAttr(attrs, burst.resource, "cursor.team.id", "coding_agent.cursor.team.id")
	copyIntAttr(attrs, burst.resource, "cursor.user.id", "coding_agent.cursor.user.id")
	putAggregateUsage(attrs, events)
}

func appendChatSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []Event) {
	errored := map[string]bool{}
	corrections := map[string][]Event{}
	for _, event := range events {
		if event.UsageEventID == "" {
			continue
		}
		if event.Body == BodyAPIError {
			errored[event.UsageEventID] = true
		} else if IsCorrectionBody(event.Body) {
			corrections[event.UsageEventID] = append(corrections[event.UsageEventID], event)
		}
	}
	for _, event := range events {
		if event.Body != BodyAPIRequest {
			continue
		}
		span := spans.AppendEmpty()
		name := "chat"
		if model := StringValue(event.Attrs["cursor.model.name"]); model != "" {
			name += " " + model
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
		span.SetName(name)
		span.SetTraceID(traceID)
		span.SetSpanID(deterministicSpanID(traceID, "chat:"+event.EventID))
		span.SetParentSpanID(parentID)
		span.SetKind(ptrace.SpanKindClient)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("coding_agent.source.event", event.Body)
		for _, m := range chatTokenAttrs {
			copyIntAttr(span.Attributes(), event.Attrs, m.source, m.dest)
		}
		copyBoolAttr(span.Attributes(), event.Attrs, "cursor.api.billable", "coding_agent.cursor.billable")
		if event.UsageEventID != "" {
			if errored[event.UsageEventID] {
				span.Status().SetCode(ptrace.StatusCodeError)
				span.Status().SetMessage("model request errored")
			}
			for _, correction := range corrections[event.UsageEventID] {
				e := span.Events().AppendEmpty()
				e.SetName(correction.Body)
				e.SetTimestamp(pcommon.NewTimestampFromTime(correction.Timestamp))
				copyStringAttr(e.Attributes(), correction.Attrs, "cursor.api.correction.kind", "coding_agent.cursor.correction.kind")
				e.Attributes().PutStr("coding_agent.cursor.usage_event_id", event.UsageEventID)
			}
		}
	}
}

// appendRootEvents lands every non-chat record the chat spans did not consume:
// unjoined api errors and corrections (their request sat in an earlier,
// already-finalized burst), skill and hook records, cloud-agent lifecycle
// records, and unknown bodies as generic id-only events.
func appendRootEvents(dst ptrace.SpanEventSlice, events []Event) {
	joined := map[string]bool{}
	for _, event := range events {
		if event.Body == BodyAPIRequest && event.UsageEventID != "" {
			joined[event.UsageEventID] = true
		}
	}
	for _, event := range events {
		if event.Body == BodyAPIRequest {
			continue
		}
		if (event.Body == BodyAPIError || IsCorrectionBody(event.Body)) && event.UsageEventID != "" && joined[event.UsageEventID] {
			continue
		}
		e := dst.AppendEmpty()
		e.SetName(event.Body)
		e.SetTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		e.Attributes().PutStr("coding_agent.cursor.event_id", event.EventID)
		switch {
		case event.Body == BodyAPIError:
			copyStringAttr(e.Attributes(), event.Attrs, "cursor.model.name", "coding_agent.cursor.model")
			copyStringAttr(e.Attributes(), event.Attrs, attrUsageEventID, "coding_agent.cursor.usage_event_id")
		case IsCorrectionBody(event.Body):
			copyStringAttr(e.Attributes(), event.Attrs, "cursor.api.correction.kind", "coding_agent.cursor.correction.kind")
			copyStringAttr(e.Attributes(), event.Attrs, attrUsageEventID, "coding_agent.cursor.usage_event_id")
		default:
			mappings := rootEventAllowlist[event.Body]
			if IsCloudAgentBody(event.Body) {
				mappings = cloudAgentAllowlist
			}
			for _, m := range mappings {
				copyStringAttr(e.Attributes(), event.Attrs, m.source, m.dest)
				copyIntAttr(e.Attributes(), event.Attrs, m.source, m.dest)
				copyBoolAttr(e.Attributes(), event.Attrs, m.source, m.dest)
			}
		}
	}
}

func deterministicTraceID(conversationID string, events []Event) pcommon.TraceID {
	firstID := ""
	if len(events) > 0 {
		firstID = events[0].EventID
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"cursor", conversationID, firstID}, "\x00")))
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

func putAggregateUsage(attrs pcommon.Map, events []Event) {
	totals := map[string]int64{}
	seen := map[string]bool{}
	for _, event := range events {
		if event.Body != BodyAPIRequest {
			continue
		}
		for _, m := range chatTokenAttrs {
			if value, ok := Int64Value(event.Attrs[m.source]); ok {
				totals[m.source] += value
				seen[m.source] = true
			}
		}
	}
	for _, m := range chatTokenAttrs {
		if seen[m.source] {
			attrs.PutInt(m.dest, totals[m.source])
		}
	}
}

func copyIntAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value, ok := Int64Value(src[from]); ok {
		dst.PutInt(to, value)
	}
}

func copyStringAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value := StringValue(src[from]); value != "" {
		dst.PutStr(to, value)
	}
}

func copyBoolAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value, ok := BoolValue(src[from]); ok {
		dst.PutBool(to, value)
	}
}
