// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"errors"
	"fmt"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
)

const (
	// SignalLogs and SignalTraces are the values the signals list accepts.
	SignalLogs   = "logs"
	SignalTraces = "traces"
)

// Config mirrors the connector's YAML: the connector-wide settings, then one
// block per harness. Each block owns its own defaults and validation; nothing
// is shared between the codex, cursor, and claude_code blocks.
type Config struct {
	CaptureIdentity bool          `mapstructure:"capture_identity"`
	Signals         []string      `mapstructure:"signals"`
	Codex           codex.Config  `mapstructure:"codex"`
	Cursor          cursor.Config `mapstructure:"cursor"`
	ClaudeCode      claude.Config `mapstructure:"claude_code"`
}

func createDefaultConfig() *Config {
	return &Config{
		CaptureIdentity: true,
		Signals:         []string{SignalLogs, SignalTraces},
		Codex:           codex.NewDefaultConfig(),
		Cursor:          cursor.NewDefaultConfig(),
		ClaudeCode:      claude.NewDefaultConfig(),
	}
}

// Validate rejects its own invalid values and each edge's, wrapping a
// sub-config error with the key it came from.
func (c *Config) Validate() error {
	if len(c.Signals) == 0 {
		return errors.New("signals must list at least one of logs, traces")
	}
	seen := map[string]bool{}
	for _, signal := range c.Signals {
		if signal != SignalLogs && signal != SignalTraces {
			return fmt.Errorf("signals: unknown signal %q (want logs or traces)", signal)
		}
		if seen[signal] {
			return fmt.Errorf("signals: %q listed twice", signal)
		}
		seen[signal] = true
	}
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

// ConsumesLogs reports whether the logs-to-traces edge is on.
func (c *Config) ConsumesLogs() bool { return c.consumes(SignalLogs) }

// ConsumesTraces reports whether the traces-to-traces edge is on.
func (c *Config) ConsumesTraces() bool { return c.consumes(SignalTraces) }

func (c *Config) consumes(signal string) bool {
	for _, s := range c.Signals {
		if s == signal {
			return true
		}
	}
	return false
}
