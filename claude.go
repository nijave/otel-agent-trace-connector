// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"

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

func (*claudeTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *claudeTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	input.CopyTo(output)
	for i := 0; i < output.ResourceSpans().Len(); i++ {
		rs := output.ResourceSpans().At(i)
		version := resourceString(rs.Resource(), "service.version")
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				normalizeClaudeSpan(spans.At(k), version)
			}
		}
	}
	return n.next.ConsumeTraces(ctx, output)
}

func normalizeClaudeSpan(span ptrace.Span, version string) {
	switch span.Name() {
	case "claude_code.interaction":
		span.SetName("invoke_agent claude_code")
		span.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		span.Attributes().PutStr("gen_ai.agent.name", "claude_code")
		putClaudeCommon(span.Attributes(), version)
		if sessionID := firstSpanString(span, "session.id", "session_id"); sessionID != "" {
			span.Attributes().PutStr("gen_ai.conversation.id", sessionID)
		}
	case "claude_code.llm_request":
		model := firstSpanString(span, "gen_ai.request.model", "model")
		span.SetName("chat" + optionalNameSuffix(model))
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		putClaudeCommon(span.Attributes(), version)
		if model != "" {
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
	case "claude_code.tool":
		tool := firstSpanString(span, "gen_ai.tool.name", "tool_name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
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
