// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"fmt"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
)

// Config mirrors the connector's YAML: the connector-wide settings, then one
// block per harness. Each block owns its own defaults and validation; nothing
// is shared between the codex, cursor, and claude_code blocks.
type Config struct {
	CaptureIdentity bool          `mapstructure:"capture_identity"`
	Codex           codex.Config  `mapstructure:"codex"`
	Cursor          cursor.Config `mapstructure:"cursor"`
	ClaudeCode      claude.Config `mapstructure:"claude_code"`
}

func createDefaultConfig() *Config {
	return &Config{
		CaptureIdentity: true,
		Codex:           codex.NewDefaultConfig(),
		Cursor:          cursor.NewDefaultConfig(),
		ClaudeCode:      claude.NewDefaultConfig(),
	}
}

// Validate rejects its own invalid values and each edge's, wrapping a
// sub-config error with the key it came from.
func (c *Config) Validate() error {
	if err := c.Codex.Validate(); err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	if err := c.Cursor.Validate(); err != nil {
		return fmt.Errorf("cursor: %w", err)
	}
	if err := c.ClaudeCode.Validate(); err != nil {
		return fmt.Errorf("claude_code: %w", err)
	}
	return nil
}
