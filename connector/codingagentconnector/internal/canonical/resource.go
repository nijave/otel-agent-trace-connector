// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package canonical

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// canonicalResourceKeys is the complete canonical resource vocabulary:
// standard OTel identity keys the canonical edge legitimately carries.
// Everything else on a resource fails closed, mirroring the span vocabulary.
var canonicalResourceKeys = []string{
	"service.name",
	"service.version",
	"telemetry.sdk.name",
	"telemetry.sdk.language",
	"telemetry.sdk.version",
}

// requiredResourceKeys lists the resource attributes every emitted canonical
// resource group must carry. service.name feeds coding_agent.client.name on
// edges that derive the client from the resource.
var requiredResourceKeys = []string{
	"service.name",
}

// IsCanonicalResourceKey reports whether key belongs to the canonical
// resource vocabulary.
func IsCanonicalResourceKey(key string) bool {
	for _, k := range canonicalResourceKeys {
		if key == k {
			return true
		}
	}
	return false
}

// FilterResource strips every resource attribute outside the canonical
// resource vocabulary from rs. When captureIdentity is false the identity
// resource keys (host.name) are stripped even if present. Edges call it after
// copying a raw input resource; reads of raw resource keys (such as
// session.id) must happen before the call.
func FilterResource(rs ptrace.ResourceSpans, captureIdentity bool) {
	rs.Resource().Attributes().RemoveIf(func(key string, _ pcommon.Value) bool {
		if !IsCanonicalResourceKey(key) {
			return true
		}
		if key == "host.name" && !captureIdentity {
			return true
		}
		return false
	})
}
