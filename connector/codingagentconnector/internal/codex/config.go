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

	// HarnessCodex is the only harness key the signals map accepts: Codex is
	// the only harness with two implementations of the same data (its logs edge
	// and its native traces edge).
	HarnessCodex = "codex"

	// SignalLogs and SignalTraces are the values a signals list accepts.
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
	// Signals selects, per harness, which input signals the connector consumes
	// for that harness. It exists only to choose between duplicate
	// implementations of the same data on different signals; the only key today
	// is "codex" (its logs edge and its native traces edge carry the same
	// turns). Harnesses with a single implementation are always constructed and
	// never appear here. A signal left out means that edge is not built, so its
	// records pass through the connector untouched and still reach the
	// pipeline's other exporters. Decoding a map replaces each listed harness's
	// list wholesale.
	Signals map[string][]string `mapstructure:"signals"`
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
		Signals:          map[string][]string{HarnessCodex: {SignalLogs}},
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
		return errors.New("signals must map at least one harness to its consumed signals")
	}
	for harness, signals := range c.Signals {
		if harness != HarnessCodex {
			return fmt.Errorf("signals: unknown harness %q (only %q has duplicate implementations)", harness, HarnessCodex)
		}
		if len(signals) == 0 {
			return fmt.Errorf("signals: %q must list at least one signal", harness)
		}
		seen := map[string]bool{}
		for _, signal := range signals {
			if signal != SignalLogs && signal != SignalTraces {
				return fmt.Errorf("signals: %q: unknown signal %q (want logs or traces)", harness, signal)
			}
			if seen[signal] {
				return fmt.Errorf("signals: %q: %q listed twice", harness, signal)
			}
			seen[signal] = true
		}
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

// SignalEnabled reports whether the named harness's implementation on the
// named signal is constructed. Only "codex" appears in the map, so only the
// Codex edges consult this; single-implementation edges are always built.
func (c *Config) SignalEnabled(harness, signal string) bool {
	for _, s := range c.Signals[harness] {
		if s == signal {
			return true
		}
	}
	return false
}
