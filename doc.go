// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate mdatagen metadata.yaml

// Package codingagentconnector exposes the coding-agent connector: a Codex
// logs-to-traces edge and a Claude Code traces-to-traces edge.
//
// The generated_*.go files and internal/metadata are produced by mdatagen from
// metadata.yaml. Regeneration currently requires a patched mdatagen: upstream
// derives the generated test package name from filepath.Base of the module
// directory, which is not a valid Go identifier here because the directory name
// contains hyphens. Build mdatagen with that call replaced by the real package
// name (go list -f {{.Name}}) before running go generate.
package codingagentconnector // import "github.com/nijave/otel-agent-trace-connector"
