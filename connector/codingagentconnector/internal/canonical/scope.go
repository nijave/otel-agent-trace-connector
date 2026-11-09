// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package canonical

// DefaultScopeVersion is used for the emitted instrumentation scope when the
// Collector build info does not carry a version (for example in unit tests).
// It lives here because every edge sets the same instrumentation scope, so no
// edge needs to import another to reach it.
const DefaultScopeVersion = "0.1.0"
