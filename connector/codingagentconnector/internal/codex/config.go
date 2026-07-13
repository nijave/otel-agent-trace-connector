// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"errors"
	"time"
)

const (
	defaultTurnTimeout   = 10 * time.Minute
	defaultReorderWindow = 30 * time.Second
	defaultMaxActive     = 10_000
	defaultMaxEvents     = 1_000
)

// Config controls correlation state and turn finalization.
type Config struct {
	TurnTimeout    time.Duration `mapstructure:"turn_timeout"`
	ReorderWindow  time.Duration `mapstructure:"reorder_window"`
	MaxActiveTurns int           `mapstructure:"max_active_turns"`
	MaxEvents      int           `mapstructure:"max_events_per_turn"`
}

// NewDefaultConfig returns an independent default configuration.
func NewDefaultConfig() *Config {
	return &Config{
		TurnTimeout:    defaultTurnTimeout,
		ReorderWindow:  defaultReorderWindow,
		MaxActiveTurns: defaultMaxActive,
		MaxEvents:      defaultMaxEvents,
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
	return nil
}
