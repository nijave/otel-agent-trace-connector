// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claude

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
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/content"
)

// claudeTraceNormalizer preserves Claude Code's native hierarchy and IDs while
// adding the canonical names and GenAI attributes used across coding agents.
type claudeTraceNormalizer struct {
	next            consumer.Traces
	captureIdentity bool
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Claude Code traces-to-traces edge.
func New(next consumer.Traces, captureIdentity bool) connector.Traces {
	return &claudeTraceNormalizer{next: next, captureIdentity: captureIdentity}
}

func (*claudeTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeTraces renames native claude_code.* spans and remaps their attributes
// onto the canonical vocabulary. Canonical output carries only coding-agent
// spans: spans outside the claude_code.* namespace in claimed groups are
// dropped here (the raw pipelines preserve the originals).
func (n *claudeTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		if !ContainsClaudeSpans(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.CopyTo(rs)
		version := resourceString(rs.Resource(), "service.version")
		// Read raw keys before the filter: session.id feeds conversation ids
		// but is not part of the canonical resource vocabulary.
		resourceSessionID := resourceString(rs.Resource(), "session.id")
		canonical.FilterResource(rs, n.captureIdentity)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			foreign := make(map[pcommon.SpanID]bool, spans.Len())
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if !strings.HasPrefix(span.Name(), claudeSpanPrefix) {
					foreign[span.SpanID()] = true
					continue
				}
				normalizeClaudeSpan(span, version, resourceSessionID, n.captureIdentity)
				content.Strip(span)
			}
			spans.RemoveIf(func(s ptrace.Span) bool { return foreign[s.SpanID()] })
		}
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool { return ss.Spans().Len() == 0 })
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// claudeSpanPrefix is the namespace Claude Code gives every span it emits. Matching
// the namespace rather than the three renamed span types keeps batches that hold
// only sub-spans such as claude_code.tool.execution: Claude Code exports a span when
// it ends, so a child can land in an export that carries none of its ancestors, and
// dropping that batch would delete those spans from the trace.
const claudeSpanPrefix = "claude_code."

// ContainsClaudeSpans reports whether any span in the group carries Claude
// Code's native span-name namespace. The GenAI edge also calls this to leave
// Claude groups to the Claude normalizer.
func ContainsClaudeSpans(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		spans := resourceSpans.ScopeSpans().At(i).Spans()
		for j := 0; j < spans.Len(); j++ {
			if strings.HasPrefix(spans.At(j).Name(), claudeSpanPrefix) {
				return true
			}
		}
	}
	return false
}

func normalizeClaudeSpan(span ptrace.Span, version, resourceSessionID string, captureIdentity bool) {
	switch span.Name() {
	case "claude_code.interaction":
		span.SetName("invoke_agent claude_code")
		span.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		span.Attributes().PutStr("gen_ai.agent.name", "claude_code")
		span.Attributes().PutStr("coding_agent.source.event", "claude_code.interaction")
		putClaudeCommon(span.Attributes(), version)
		sessionID := firstSpanString(span, "session.id", "session_id")
		if sessionID == "" {
			sessionID = resourceSessionID
		}
		if sessionID != "" {
			span.Attributes().PutStr("gen_ai.conversation.id", sessionID)
		}
		if terminal := firstSpanString(span, "terminal.type"); terminal != "" {
			span.Attributes().PutStr("coding_agent.terminal.type", terminal)
		}
		if captureIdentity {
			if uid := firstSpanString(span, "user.id"); uid != "" {
				span.Attributes().PutStr("coding_agent.user.id", uid)
			}
		}
	case "claude_code.llm_request":
		model := firstSpanString(span, "gen_ai.request.model", "model")
		span.SetName("chat" + optionalNameSuffix(model))
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("coding_agent.source.event", "claude_code.llm_request")
		putClaudeCommon(span.Attributes(), version)
		if model != "" {
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
		remapUsage(span.Attributes())
	case "claude_code.tool":
		tool := firstSpanString(span, "gen_ai.tool.name", "tool_name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
		span.Attributes().PutStr("coding_agent.source.event", "claude_code.tool")
		putClaudeCommon(span.Attributes(), version)
		if tool != "" {
			span.Attributes().PutStr("gen_ai.tool.name", tool)
		}
	default:
		// Sub-spans such as claude_code.tool.execution, blocked_on_user, or
		// hooks are not renamed but must still satisfy the required-key
		// contract before the strip.
		attrs := span.Attributes()
		operation := strings.TrimPrefix(span.Name(), claudeSpanPrefix)
		if strings.HasPrefix(operation, "tool") {
			operation = "execute_tool"
		}
		attrs.PutStr("gen_ai.operation.name", operation)
		putClaudeCommon(attrs, version)
	}
}

// usageIntKeys maps Claude Code's vendor token counters onto their canonical
// counterparts.
var usageIntKeys = [][2]string{
	{"input_tokens", "gen_ai.usage.input_tokens"},
	{"output_tokens", "gen_ai.usage.output_tokens"},
	{"cache_read_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"cache_creation_tokens", "gen_ai.usage.cache_write.input_tokens"},
}

const (
	ttftCanonicalKey      = "gen_ai.response.time_to_first_chunk"
	finishReasonsCanonKey = "gen_ai.response.finish_reasons"
)

// remapUsage copies Claude Code's usage, latency, and stop-reason attributes
// onto their canonical keys. Raw keys are not deleted here: content.Strip
// runs right after normalization and removes everything outside the
// vocabulary, raw copies included.
func remapUsage(attrs pcommon.Map) {
	for _, pair := range usageIntKeys {
		if n, ok := attrInt(attrs, pair[0]); ok {
			attrs.PutInt(pair[1], n)
		}
	}
	if n, ok := attrInt(attrs, "ttft_ms"); ok {
		// Wire units are integer milliseconds; canonical stores seconds.
		attrs.PutDouble(ttftCanonicalKey, float64(n)/1000)
	}
	if value, ok := attrs.Get("stop_reason"); ok && value.Type() == pcommon.ValueTypeStr {
		appendFinishReason(attrs, value.Str())
	}
}

// appendFinishReason adds reason to gen_ai.response.finish_reasons unless the
// slice already carries it; Claude Code also emits that canonical key itself.
func appendFinishReason(attrs pcommon.Map, reason string) {
	if reason == "" {
		return
	}
	if existing, ok := attrs.Get(finishReasonsCanonKey); ok && existing.Type() == pcommon.ValueTypeSlice {
		reasons := existing.Slice()
		for i := 0; i < reasons.Len(); i++ {
			if reasons.At(i).Str() == reason {
				return
			}
		}
		reasons.AppendEmpty().SetStr(reason)
		return
	}
	reasons := attrs.PutEmptySlice(finishReasonsCanonKey)
	reasons.AppendEmpty().SetStr(reason)
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

func putClaudeCommon(attrs pcommon.Map, version string) {
	attrs.PutStr("gen_ai.provider.name", "anthropic")
	attrs.PutStr("coding_agent.client.name", "claude_code")
	attrs.PutStr("coding_agent.source", "native")
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
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

var _ connector.Traces = (*claudeTraceNormalizer)(nil)
