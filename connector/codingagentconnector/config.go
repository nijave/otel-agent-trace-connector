// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"

// Config controls Codex log correlation. It is an alias so OCB decoding,
// validation, and public configuration documentation stay on the component.
type Config = codex.Config

func createDefaultConfig() *Config { return codex.NewDefaultConfig() }
