// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"errors"
	"fmt"
	"time"
)

const (
	defaultTurnTimeout   = 10 * time.Minute
	defaultReorderWindow = 30 * time.Second
	defaultMaxActive     = 10_000
	defaultMaxEvents     = 1_000

	// SignalLogs and SignalTraces are the values the signals list accepts.
	SignalLogs   = "logs"
	SignalTraces = "traces"

	// ClaudeChatTraces and ClaudeChatLogs are the values claude_chat_source
	// accepts. The empty value decodes as ClaudeChatTraces.
	ClaudeChatTraces = "traces"
	ClaudeChatLogs   = "logs"
)

// Config controls correlation state, turn finalization, and which input
// signals the connector consumes.
type Config struct {
	TurnTimeout     time.Duration `mapstructure:"turn_timeout"`
	ReorderWindow   time.Duration `mapstructure:"reorder_window"`
	MaxActiveTurns  int           `mapstructure:"max_active_turns"`
	MaxEvents       int           `mapstructure:"max_events_per_turn"`
	CaptureIdentity bool          `mapstructure:"capture_identity"`
	// Signals lists the input signals the connector consumes: "logs",
	// "traces", or both. A signal left out passes through the connector
	// untouched, so its records still reach the pipeline's other exporters.
	Signals []string `mapstructure:"signals"`
	// ClaudeChatSource picks the signal that emits canonical Claude Code chat
	// spans: "traces" (the default) keeps the native claude_code.llm_request
	// spans as the chat source; "logs" drops those spans and synthesizes chat
	// spans from claude_code.api_request / claude_code.api_error log records
	// instead. Exactly one source emits chat, so canonical output never
	// duplicates a request. A deployment that splits Claude Code logs and
	// traces across two connector instances must set the same value on both.
	ClaudeChatSource string `mapstructure:"claude_chat_source"`
}

// NewDefaultConfig returns an independent default configuration.
func NewDefaultConfig() *Config {
	return &Config{
		TurnTimeout:      defaultTurnTimeout,
		ReorderWindow:    defaultReorderWindow,
		MaxActiveTurns:   defaultMaxActive,
		MaxEvents:        defaultMaxEvents,
		CaptureIdentity:  true,
		Signals:          []string{SignalLogs, SignalTraces},
		ClaudeChatSource: ClaudeChatTraces,
	}
}

// Validate rejects values which would make state unbounded or finalization ambiguous.
func (c *Config) Validate() error {
	if c.TurnTimeout <= 0 {
		return errors.New("turn_timeout must be positive")
	}
	if c.ReorderWindow < 0 {
		return errors.New("reorder_window must not be negative")
	}
	if c.ReorderWindow >= c.TurnTimeout {
		return errors.New("reorder_window must be less than turn_timeout")
	}
	if c.MaxActiveTurns <= 0 {
		return errors.New("max_active_turns must be positive")
	}
	if c.MaxEvents <= 0 {
		return errors.New("max_events_per_turn must be positive")
	}
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
	switch c.ClaudeChatSource {
	case "", ClaudeChatTraces, ClaudeChatLogs:
	default:
		return fmt.Errorf("claude_chat_source: unknown value %q (want traces or logs)", c.ClaudeChatSource)
	}
	return nil
}

// ClaudeChatsFromLogs reports whether canonical Claude Code chat spans come
// from the logs signal instead of the native traces signal.
func (c *Config) ClaudeChatsFromLogs() bool { return c.ClaudeChatSource == ClaudeChatLogs }

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
