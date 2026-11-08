// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package claudelogs synthesizes canonical Claude Code chat spans from the
// harness's structured log events (scope com.anthropic.claude_code.events).
// The logs wire is the only Claude Code surface that carries the per-request
// cost and API failures, so under claude_chat_source: logs this edge replaces
// the traces edge's claude_code.llm_request spans as the chat source. The
// edge is stateless: one canonical span per claimed record, no sweep loop,
// no turn state.
package claudelogs

import (
	"context"
	"crypto/sha256"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

const (
	// ScopeName is the instrumentation scope Claude Code gives its log
	// events. Only records inside this scope can be claimed.
	ScopeName = "com.anthropic.claude_code.events"

	// eventNameRequest and eventNameError are the event.name values the edge
	// claims; every other Claude event (user_prompt, tool_result, …) is
	// ignored.
	eventNameRequest = "api_request"
	eventNameError   = "api_error"

	// instrumentationScope names the connector's own emitted scope, the same
	// pattern the other synthesizing edges use.
	instrumentationScope = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claudelogs"
)

// edge is the stateless Claude Code logs-to-traces chat edge. It holds no
// state between batches: every claimed record becomes exactly one canonical
// span, so replays are idempotent.
type edge struct {
	next            consumer.Traces
	captureIdentity bool
	chatFromLogs    bool
	scopeVersion    string
}

// New creates the Claude Code logs edge. With the default
// claude_chat_source: traces it consumes nothing (the native traces edge owns
// chat spans); with claude_chat_source: logs it synthesizes chat spans from
// api_request and api_error records.
func New(cfg *codex.Config, set connector.Settings, next consumer.Traces) connector.Logs {
	version := set.BuildInfo.Version
	if version == "" {
		version = codex.DefaultScopeVersion
	}
	return &edge{
		next:            next,
		captureIdentity: cfg.CaptureIdentity,
		chatFromLogs:    cfg.ClaudeChatsFromLogs(),
		scopeVersion:    version,
	}
}

func (*edge) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// Start and Shutdown are no-ops: the edge holds no state to sweep or drain.
func (*edge) Start(context.Context, component.Host) error { return nil }
func (*edge) Shutdown(context.Context) error              { return nil }

// ConsumeLogs synthesizes one canonical chat span per claimed record.
// Records without trace context (a zero TraceId or SpanId), without a
// request_id, or without a usable timestamp drop: clients that export logs
// without traces are out of scope — the synthesized span joins the native
// trace through the log record's ids, and without them the span would float
// disconnected in canonical output. The same goes for records without a
// request_id: it determines the span id. A dropped record leaves no empty
// resource/scope group behind: each group is created only when a usable span
// exists in it.
func (e *edge) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	if !e.chatFromLogs {
		return nil
	}
	output := ptrace.NewTraces()
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		rl := logs.ResourceLogs().At(i)
		// Read raw resource keys before the filter: service.version feeds
		// coding_agent.client.version but the filter keeps the canonical
		// resource vocabulary only.
		version := resourceString(rl.Resource(), "service.version")
		var spans ptrace.SpanSlice
		rsCreated := false
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			scopeName := sl.Scope().Name()
			for k := 0; k < sl.LogRecords().Len(); k++ {
				record := sl.LogRecords().At(k)
				if !claim(scopeName, record) {
					continue
				}
				// Build fully before creating the output group, so a rejected
				// record can never leave an empty resource/scope group behind.
				span, ok := newChatSpan(record, version, e.captureIdentity)
				if !ok {
					continue
				}
				if !rsCreated {
					rs := output.ResourceSpans().AppendEmpty()
					if err := rs.Resource().Attributes().FromRaw(rl.Resource().Attributes().AsRaw()); err != nil {
						return err
					}
					canonical.FilterResource(rs, e.captureIdentity)
					ss := rs.ScopeSpans().AppendEmpty()
					ss.Scope().SetName(instrumentationScope)
					ss.Scope().SetVersion(e.scopeVersion)
					spans = ss.Spans()
					rsCreated = true
				}
				span.CopyTo(spans.AppendEmpty())
			}
		}
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return e.next.ConsumeTraces(ctx, output)
}

// claim reports whether one log record becomes a chat span: the Claude events
// scope and one of the two claimed event names.
func claim(scopeName string, record plog.LogRecord) bool {
	if scopeName != ScopeName {
		return false
	}
	name, ok := record.Attributes().Get("event.name")
	if !ok {
		return false
	}
	return name.Str() == eventNameRequest || name.Str() == eventNameError
}

// newChatSpan builds the canonical chat span for one claimed record as a
// standalone span the caller appends to its output. ok is false when the
// record lacks the context the span needs (trace ids, request_id, or a usable
// timestamp); callers must then not create any output group for it — the
// contract forbids empty resource/scope groups.
func newChatSpan(record plog.LogRecord, version string, captureIdentity bool) (ptrace.Span, bool) {
	attrs := record.Attributes()
	traceID := record.TraceID()
	parentID := record.SpanID()
	if traceID.IsEmpty() || parentID.IsEmpty() {
		return ptrace.Span{}, false
	}
	requestID := attrString(attrs, "request_id")
	if requestID == "" {
		return ptrace.Span{}, false
	}
	end := record.Timestamp().AsTime()
	if record.Timestamp() == 0 {
		parsed, ok := attrTime(attrs, "event.timestamp")
		if !ok {
			return ptrace.Span{}, false
		}
		end = parsed
	}
	model := attrString(attrs, "model")
	isError := attrString(attrs, "event.name") == eventNameError

	span := ptrace.NewSpan()
	name := "chat"
	if model != "" {
		name += " " + model
	}
	span.SetName(name)
	span.SetTraceID(traceID)
	span.SetSpanID(chatSpanID(requestID))
	span.SetParentSpanID(parentID)
	span.SetKind(ptrace.SpanKindClient)
	start := end
	if ms, ok := attrInt(attrs, "duration_ms"); ok && ms > 0 {
		start = end.Add(-time.Duration(ms) * time.Millisecond)
	}
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(end))

	out := span.Attributes()
	// Connector-written keys match the Claude traces edge.
	out.PutStr("gen_ai.operation.name", "chat")
	out.PutStr("gen_ai.provider.name", "anthropic")
	out.PutStr("coding_agent.source", "native")
	if body := record.Body(); body.Type() == pcommon.ValueTypeStr && body.Str() != "" {
		out.PutStr("coding_agent.source.event", body.Str())
	}
	out.PutStr("coding_agent.client.name", "claude_code")
	if version != "" {
		out.PutStr("coding_agent.client.version", version)
	}
	// Wire mapping: canonical vocabulary only, everything else drops.
	if model != "" {
		out.PutStr("gen_ai.request.model", model)
	}
	out.PutStr("gen_ai.response.id", requestID)
	for _, pair := range counterKeys {
		if n, ok := attrInt(attrs, pair[0]); ok {
			out.PutInt(pair[1], n)
		}
	}
	if ms, ok := attrInt(attrs, "ttft_ms"); ok {
		// Wire units are integer milliseconds; canonical stores seconds.
		out.PutDouble(ttftCanonicalKey, float64(ms)/1000)
	}
	if effort := attrString(attrs, "effort"); effort != "" {
		out.PutStr("gen_ai.request.reasoning.level", effort)
	}
	// A zero cost maps as 0; an unparsable one is absent, not an error.
	if cost, ok := attrDouble(attrs, "cost_usd"); ok {
		out.PutDouble(costCanonicalKey, cost)
	}
	if conversation := attrString(attrs, "session.id"); conversation != "" {
		out.PutStr("gen_ai.conversation.id", conversation)
	}
	if agentID := attrString(attrs, "agent_id"); agentID != "" {
		out.PutStr("gen_ai.agent.id", agentID)
	}
	if terminal := attrString(attrs, "terminal.type"); terminal != "" {
		out.PutStr("coding_agent.terminal.type", terminal)
	}
	if captureIdentity {
		if uid := attrString(attrs, "user.id"); uid != "" {
			out.PutStr("coding_agent.user.id", uid)
		}
		if email := attrString(attrs, "user.email"); email != "" {
			out.PutStr("coding_agent.user.email", email)
		}
		// organization.id is the Anthropic organization, the billing-scale
		// group; Cursor's team id is the same scale.
		if org := attrString(attrs, "organization.id"); org != "" {
			out.PutStr("coding_agent.team.id", org)
		}
	}
	if isError {
		span.Status().SetCode(ptrace.StatusCodeError)
		if msg := attrString(attrs, "error"); msg != "" {
			span.Status().SetMessage(msg)
		}
	}
	return span, true
}

// counterKeys maps the api_request token counters onto their canonical
// destinations. The 5m/1h cache-creation splits stay vendor-side: only the
// total cache_creation_tokens maps.
var counterKeys = [][2]string{
	{"input_tokens", "gen_ai.usage.input_tokens"},
	{"output_tokens", "gen_ai.usage.output_tokens"},
	{"cache_read_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"cache_creation_tokens", "gen_ai.usage.cache_write.input_tokens"},
}

const (
	ttftCanonicalKey = "gen_ai.response.time_to_first_chunk"
	costCanonicalKey = "coding_agent.usage.cost"
)

// chatSpanID derives the synthesized span id from the request id: first 8
// bytes of SHA-256, deterministic so replays produce the same id.
func chatSpanID(requestID string) pcommon.SpanID {
	sum := sha256.Sum256([]byte(requestID))
	var id pcommon.SpanID
	copy(id[:], sum[:8])
	return id
}

// attrString reads a string attribute, empty when absent or non-string.
func attrString(attrs pcommon.Map, key string) string {
	value, ok := attrs.Get(key)
	if !ok || value.Type() != pcommon.ValueTypeStr {
		return ""
	}
	return value.Str()
}

// attrInt coerces an attribute to int64 following the connector-wide
// semantics: ints pass through, doubles truncate, strings parse as integers.
func attrInt(attrs pcommon.Map, key string) (int64, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return 0, false
	}
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return value.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(value.Double()), true
	case pcommon.ValueTypeStr:
		parsed, err := strconv.ParseInt(value.Str(), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

// attrDouble coerces an attribute to float64: ints and doubles pass through,
// strings parse as floats.
func attrDouble(attrs pcommon.Map, key string) (float64, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return 0, false
	}
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return float64(value.Int()), true
	case pcommon.ValueTypeDouble:
		return value.Double(), true
	case pcommon.ValueTypeStr:
		parsed, err := strconv.ParseFloat(value.Str(), 64)
		return parsed, err == nil
	}
	return 0, false
}

// attrTime reads a timestamp attribute: an RFC3339 string or an integer
// count of Unix nanoseconds.
func attrTime(attrs pcommon.Map, key string) (time.Time, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return time.Time{}, false
	}
	switch value.Type() {
	case pcommon.ValueTypeStr:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, value.Str()); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	case pcommon.ValueTypeInt:
		return pcommon.Timestamp(value.Int()).AsTime(), true
	}
	return time.Time{}, false
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok || value.Type() != pcommon.ValueTypeStr {
		return ""
	}
	return value.Str()
}

var _ connector.Logs = (*edge)(nil)
