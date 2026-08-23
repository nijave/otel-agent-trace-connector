// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate mdatagen metadata.yaml

// Package codingagentconnector exposes the coding-agent connector: a logs
// edge correlating Codex and Cursor into canonical traces, and a traces edge
// normalizing Claude Code, GenAI-semconv sources (openai-v2, util-genai,
// Strands, GitHub Copilot), OpenCode, and Pi. The generated_*.go
// files and internal/metadata are produced by mdatagen from metadata.yaml.
package codingagentconnector // import "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"
