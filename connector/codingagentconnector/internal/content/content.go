// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package content removes prompt/completion/tool content from spans so it
// never reaches canonical output. Every normalizer edge runs it over every
// span it emits, including spans from sibling instrumentation scopes swept
// into a claimed resource group. Canonical output is restricted to the
// allowlist of benign attributes owned by internal/canonical, so content
// never reaches it — not even from unknown vendor namespaces.
package content

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// contentEventNames are span events removed entirely, attributes included.
var contentEventNames = map[string]bool{
	"gen_ai.client.inference.operation.details": true,
	"gen_ai.user.message":                       true,
	"gen_ai.assistant.message":                  true,
	"gen_ai.system.message":                     true,
	"gen_ai.tool.message":                       true,
	"gen_ai.choice":                             true,
	"memory.query":                              true,
	"memory.content":                            true,
}

// Strip reduces a span's attributes to the canonical allowlist, removes
// content-bearing span events entirely, and applies the same allowlist to
// surviving events' attributes.
func Strip(span ptrace.Span) {
	stripAttributes(span.Attributes())
	span.Events().RemoveIf(func(event ptrace.SpanEvent) bool {
		if contentEventNames[event.Name()] {
			return true
		}
		stripAttributes(event.Attributes())
		return false
	})
}

// IsContentEvent reports whether a span event name carries prompt,
// completion, or tool content and is removed from canonical output.
func IsContentEvent(name string) bool {
	return contentEventNames[name]
}

// stripAttributes reduces an attribute map to the canonical allowlist.
func stripAttributes(attributes pcommon.Map) {
	attributes.RemoveIf(func(key string, _ pcommon.Value) bool {
		return !isCanonicalAttribute(key)
	})
}

func isCanonicalAttribute(key string) bool {
	return canonical.IsCanonicalAttribute(key)
}
