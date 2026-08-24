// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package canonical holds the canonical attribute contract as data: the
// shared vocabulary every harness edge must emit within, the required-key
// set, and a conformance runner that checks normalized output against raw
// input signals.
package canonical

import "strings"

// canonicalAttributeKeys is the complete canonical vocabulary: enumerated
// gen_ai.* keys (including the six explicit usage keys — there is no
// gen_ai.usage. wildcard), server identity, and the connector-owned
// coding_agent.* provenance namespace. Everything else is a vendor key and
// must never reach canonical output.
var canonicalAttributeKeys = []string{
	"gen_ai.operation.name",
	"gen_ai.provider.name",
	"gen_ai.request.model",
	"gen_ai.request.max_tokens",
	"gen_ai.request.stream",
	"gen_ai.response.finish_reasons",
	"gen_ai.response.id",
	"gen_ai.response.model",
	"gen_ai.response.time_to_first_chunk",
	"gen_ai.agent.id",
	"gen_ai.agent.name",
	"gen_ai.agent.version",
	"gen_ai.conversation.id",
	"gen_ai.tool.call.id",
	"gen_ai.tool.name",
	"gen_ai.tool.type",
	"gen_ai.tool.status",
	"gen_ai.event.start_time",
	"gen_ai.event.end_time",
	"gen_ai.usage.input_tokens",
	"gen_ai.usage.output_tokens",
	"gen_ai.usage.total_tokens",
	"gen_ai.usage.cache_read.input_tokens",
	"gen_ai.usage.cache_creation.input_tokens",
	"gen_ai.usage.reasoning.output_tokens",
	"server.address",
	"server.port",
	"exception.type",
	"exception.message",
	"exception.escaped",
	"exception.stacktrace",
	"coding_agent.source",
	"coding_agent.source.scope",
	"coding_agent.source.event",
	"coding_agent.client.name",
	"coding_agent.client.version",
}

// canonicalAttributePrefixes are safe attribute families too large to
// enumerate: exception details are standard OTel companions on error spans.
var canonicalAttributePrefixes = []string{
	"exception.",
}

var requiredKeys = []string{
	"gen_ai.operation.name",
	"coding_agent.source",
	"coding_agent.client.name",
}

// IsCanonicalAttribute reports whether key belongs to the canonical
// vocabulary: an exact enumerated key or an allowed prefix family.
func IsCanonicalAttribute(key string) bool {
	for _, k := range canonicalAttributeKeys {
		if key == k {
			return true
		}
	}
	for _, prefix := range canonicalAttributePrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// RequiredKeys returns the attributes every emitted canonical span must carry.
func RequiredKeys() []string {
	return append([]string(nil), requiredKeys...)
}
