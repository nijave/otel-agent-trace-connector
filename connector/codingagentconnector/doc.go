// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate mdatagen metadata.yaml

// Package codingagentconnector exposes the coding-agent connector: a Codex
// logs-to-traces edge and a Claude Code traces-to-traces edge. The generated_*.go
// files and internal/metadata are produced by mdatagen from metadata.yaml.
package codingagentconnector // import "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"
