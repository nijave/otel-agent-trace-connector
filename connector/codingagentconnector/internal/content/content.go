// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package content removes prompt/completion/tool content from spans so it
// never reaches canonical output. Every normalizer edge runs it over every
// span it emits, including spans from sibling instrumentation scopes swept
// into a claimed resource group. Canonical output is restricted to an
// allowlist of benign attributes, so content never reaches it — not even
// from unknown vendor namespaces.
package content

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// canonicalAttributeKeys are the only span attributes allowed to reach
// canonical output: identity, provenance, and operational metadata written by
// the normalizer edges or carried benignly by the claimed scopes. Everything
// else — unknown vendor namespaces, legacy layouts, future semconv keys —
// fails closed instead of riding along.
var canonicalAttributeKeys = map[string]bool{
	// Written by the normalizers for every claimed span.
	"telemetry.source":            true,
	"coding_agent.source.scope":   true,
	"coding_agent.client.name":    true,
	"coding_agent.client.version": true,

	// GenAI request/response and agent/tool metadata carried by the fixtures.
	"gen_ai.operation.name":               true,
	"gen_ai.provider.name":                true,
	"gen_ai.request.model":                true,
	"gen_ai.request.max_tokens":           true,
	"gen_ai.request.stream":               true,
	"gen_ai.response.finish_reasons":      true,
	"gen_ai.response.id":                  true,
	"gen_ai.response.model":               true,
	"gen_ai.response.time_to_first_chunk": true,
	"gen_ai.agent.id":                     true,
	"gen_ai.agent.name":                   true,
	"gen_ai.agent.version":                true,
	"gen_ai.conversation.id":              true,
	"gen_ai.tool.call.id":                 true,
	"gen_ai.tool.name":                    true,
	"gen_ai.tool.type":                    true,
	"gen_ai.tool.status":                  true,
	"gen_ai.server.time_to_first_token":   true,
	"gen_ai.event.start_time":             true,
	"gen_ai.event.end_time":               true,

	// Server identity per the GenAI semantic conventions.
	"server.address": true,
	"server.port":    true,

	// Strands event-loop correlation IDs and exception event details.
	"event_loop.cycle_id":        true,
	"event_loop.parent_cycle_id": true,
	"exception.type":             true,
	"exception.message":          true,
	"exception.escaped":          true,
	"exception.stacktrace":       true,

	// Copilot operational signal: billing counters, latency, correlation IDs,
	// hook decisions, and session usage-event metrics.
	"github.copilot.cost":                        true,
	"github.copilot.aiu":                         true,
	"github.copilot.turn_id":                     true,
	"github.copilot.interaction_id":              true,
	"github.copilot.turn_count":                  true,
	"github.copilot.server_duration":             true,
	"github.copilot.hook.decision":               true,
	"github.copilot.token_limit":                 true,
	"github.copilot.current_tokens":              true,
	"github.copilot.messages_length":             true,
	"github.copilot.total_premium_requests":      true,
	"github.copilot.user.message.source":         true,
	"github.copilot.user.message.interaction_id": true,
}

// canonicalAttributePrefixes are safe attribute families too large to
// enumerate: emitters mint new usage counters freely, and every key below
// gen_ai.usage. is a token count.
var canonicalAttributePrefixes = []string{
	"gen_ai.usage.",
}

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
	if canonicalAttributeKeys[key] {
		return true
	}
	for _, prefix := range canonicalAttributePrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
