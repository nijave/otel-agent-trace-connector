// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

const instrumentationScope = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"

// chatTokenAttrs maps the api_request token ints to their canonical
// destinations.
var chatTokenAttrs = []struct{ source, dest string }{
	{"cursor.api.request.input_tokens", "gen_ai.usage.input_tokens"},
	{"cursor.api.request.output_tokens", "gen_ai.usage.output_tokens"},
	{"cursor.api.request.cache_read_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"cursor.api.request.cache_creation_tokens", "gen_ai.usage.cache_write.input_tokens"},
}

func buildTrace(burst *burstState, reason, scopeVersion string, captureIdentity bool) (ptrace.Traces, error) {
	events := append([]Event(nil), burst.events...)
	// Tie-break equal timestamps on the dedupe key so a reordered at-least-once
	// batch still picks the same anchor event, and therefore the same trace id.
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].EventID < events[j].EventID
		}
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	traceID := deterministicTraceID(burst.conversationID, events)
	rootID := deterministicSpanID(traceID, "root")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	resErr := rs.Resource().Attributes().FromRaw(burst.resource)
	canonical.FilterResource(rs, captureIdentity)
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
	putRootAttributes(root.Attributes(), burst, events)
	if reason == "timeout" {
		root.Status().SetCode(ptrace.StatusCodeError)
		root.Status().SetMessage("burst closed after turn_timeout")
	}

	appendChatSpans(ss.Spans(), traceID, rootID, events)
	appendRootEvents(root.Events(), events)
	if resErr != nil {
		return traces, fmt.Errorf("copy burst resource attributes: %w", resErr)
	}
	return traces, nil
}

func putRootAttributes(attrs pcommon.Map, burst *burstState, events []Event) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "cursor")
	attrs.PutStr("gen_ai.conversation.id", burst.conversationID)
	attrs.PutStr("coding_agent.client.name", "cursor")
	if len(events) > 0 {
		attrs.PutStr("coding_agent.source.event", events[0].Body)
	}
	attrs.PutStr("coding_agent.source", "normalized")
	// Deliberately no gen_ai.provider.name: the wire never names the upstream
	// provider and the connector does not guess one. Deliberately no
	// completion marker either: quiet closing cannot distinguish a finished
	// model turn from an abandoned one. The close reason survives only as the
	// timeout span status and the turns_emitted metric label.
	copyStringAttr(attrs, burst.resource, "service.version", "coding_agent.client.version")
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
		span.Attributes().PutStr("coding_agent.source", "normalized")
		span.Attributes().PutStr("coding_agent.client.name", "cursor")
		span.Attributes().PutStr("coding_agent.source.event", event.Body)
		for _, m := range chatTokenAttrs {
			copyIntAttr(span.Attributes(), event.Attrs, m.source, m.dest)
		}
		if event.UsageEventID != "" {
			if errored[event.UsageEventID] {
				span.Status().SetCode(ptrace.StatusCodeError)
				span.Status().SetMessage("model request errored")
			}
			for _, correction := range corrections[event.UsageEventID] {
				e := span.Events().AppendEmpty()
				e.SetName(correction.Body)
				e.SetTimestamp(pcommon.NewTimestampFromTime(correction.Timestamp))
			}
		}
	}
}

// appendRootEvents lands every non-chat record the chat spans did not consume:
// unjoined api errors and corrections (their request sat in an earlier,
// already-finalized burst) and unknown bodies. Events carry their names and
// timestamps only — correction and error kinds stay readable in the event
// name itself ("api_correction_<kind>").
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
