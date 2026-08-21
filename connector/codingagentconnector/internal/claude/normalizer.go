// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// claudeTraceNormalizer preserves Claude Code's native hierarchy and IDs while
// adding the canonical names and GenAI attributes used across coding agents.
type claudeTraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Claude Code traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &claudeTraceNormalizer{next: next}
}

func (*claudeTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

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
		resourceSessionID := resourceString(rs.Resource(), "session.id")
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				normalizeClaudeSpan(spans.At(k), version, resourceSessionID)
			}
		}
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

func normalizeClaudeSpan(span ptrace.Span, version, resourceSessionID string) {
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
	case "claude_code.llm_request":
		model := firstSpanString(span, "gen_ai.request.model", "model")
		span.SetName("chat" + optionalNameSuffix(model))
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("coding_agent.source.event", "claude_code.llm_request")
		putClaudeCommon(span.Attributes(), version)
		if model != "" {
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
	case "claude_code.tool":
		tool := firstSpanString(span, "gen_ai.tool.name", "tool_name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
		span.Attributes().PutStr("coding_agent.source.event", "claude_code.tool")
		putClaudeCommon(span.Attributes(), version)
		if tool != "" {
			span.Attributes().PutStr("gen_ai.tool.name", tool)
		}
	}
}

func putClaudeCommon(attrs pcommon.Map, version string) {
	attrs.PutStr("gen_ai.provider.name", "anthropic")
	attrs.PutStr("coding_agent.client.name", "claude_code")
	attrs.PutStr("telemetry.source", "native")
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
