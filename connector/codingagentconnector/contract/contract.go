// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package contract is the public window on the canonical-output predicates:
// internal/content and internal/canonical own the strip and allowlist
// contracts but cannot be imported across this module's boundary, so
// out-of-module checkers such as the e2e validator read them through here.
// Every predicate delegates to its owner; nothing is duplicated.
package contract

import (
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/content"
)

// IsContentEvent reports whether a span event name carries prompt,
// completion, or tool content and is removed from canonical output.
func IsContentEvent(name string) bool {
	return content.IsContentEvent(name)
}

// IsCanonicalAttribute reports whether a span attribute key belongs to the
// canonical vocabulary; canonical output must carry nothing else.
func IsCanonicalAttribute(key string) bool {
	return canonical.IsCanonicalAttribute(key)
}
