// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package genai normalizes GenAI-semantic-convention native traces (the
// opentelemetry-instrumentation-openai-v2 package in both semconv modes,
// direct opentelemetry-util-genai users, and Strands Agents SDK) into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change, and
// content-bearing attributes and events never reach canonical output.
package genai

import (
	"context"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
)

// scopePrefixes lists the instrumentation-scope names this edge claims.
// Prefixes rather than exact names: upstream is renaming
// opentelemetry-instrumentation-openai-v2 to
// opentelemetry-instrumentation-genai-openai, and util-genai emits from a
// module whose path may shift below opentelemetry.util.genai.
var scopePrefixes = []string{
	"opentelemetry.instrumentation.openai_v2",
	"opentelemetry.util.genai",
	"opentelemetry.instrumentation.genai",
	"strands.telemetry",
}

type genAITraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless GenAI-semconv traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &genAITraceNormalizer{next: next}
}

func (*genAITraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *genAITraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		// Claude groups belong to the Claude normalizer even when they also
		// carry GenAI scopes; claiming here would emit the group twice.
		if claude.ContainsClaudeSpans(inputResourceSpans) {
			continue
		}
		if !containsGenAIScopes(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.CopyTo(rs)
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

func containsGenAIScopes(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		if matchesGenAIScope(resourceSpans.ScopeSpans().At(i).Scope().Name()) {
			return true
		}
	}
	return false
}

func matchesGenAIScope(name string) bool {
	for _, prefix := range scopePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

var _ connector.Traces = (*genAITraceNormalizer)(nil)
