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
)

// Config controls Codex turn correlation state and turn finalization, and
// which of Codex's duplicate implementations is built. It maps to the
// connector's `codex:` block.
type Config struct {
	TurnTimeout    time.Duration `mapstructure:"turn_timeout"`
	ReorderWindow  time.Duration `mapstructure:"reorder_window"`
	MaxActiveTurns int           `mapstructure:"max_active_turns"`
	MaxEvents      int           `mapstructure:"max_events_per_turn"`
	// Signals selects which of Codex's two implementations of the same data —
	// the logs edge and the native traces edge — is built. Codex is the only
	// harness with duplicates, so the switch lives here rather than connector
	// wide. A signal left out means that edge is not constructed, so its records
	// pass through the connector untouched and still reach the pipeline's other
	// exporters. A configured list replaces the default wholesale.
	Signals []string `mapstructure:"signals"`
}

// NewDefaultConfig returns an independent default configuration.
func NewDefaultConfig() Config {
	return Config{
		TurnTimeout:    defaultTurnTimeout,
		ReorderWindow:  defaultReorderWindow,
		MaxActiveTurns: defaultMaxActive,
		MaxEvents:      defaultMaxEvents,
		Signals:        []string{SignalLogs},
	}
}

// SignalEnabled reports whether the implementation on the named signal is
// constructed. The routers consult it only for Codex, the one harness with
// duplicates; the default list is [logs], the richer of the two.
func (c Config) SignalEnabled(signal string) bool {
	for _, s := range c.Signals {
		if s == signal {
			return true
		}
	}
	return false
}

// Validate rejects values which would make state unbounded or finalization
// ambiguous, and unknown, repeated, or absent signals.
func (c Config) Validate() error {
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
	return nil
}
