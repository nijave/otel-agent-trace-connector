// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewDefaultConfig(t *testing.T) {
	require.Equal(t, Config{
		TurnTimeout:    10 * time.Minute,
		ReorderWindow:  30 * time.Second,
		MaxActiveTurns: 10_000,
		MaxEvents:      1_000,
		Signals:        []string{SignalLogs},
	}, NewDefaultConfig())
}

// TestSignalEnabled pins the helper the routers consult: the default builds
// the logs edge and not the native traces edge, because the logs edge is the
// richer of the two duplicate implementations.
func TestSignalEnabled(t *testing.T) {
	cfg := NewDefaultConfig()
	require.True(t, cfg.SignalEnabled(SignalLogs))
	require.False(t, cfg.SignalEnabled(SignalTraces))

	cfg.Signals = []string{SignalTraces}
	require.False(t, cfg.SignalEnabled(SignalLogs))
	require.True(t, cfg.SignalEnabled(SignalTraces))
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantError string
	}{
		{name: "defaults", mutate: func(*Config) {}},
		{name: "zero reorder window", mutate: func(c *Config) { c.ReorderWindow = 0 }},
		{name: "turn timeout", mutate: func(c *Config) { c.TurnTimeout = 0 }, wantError: "turn_timeout must be positive"},
		{name: "reorder negative", mutate: func(c *Config) { c.ReorderWindow = -time.Second }, wantError: "reorder_window must not be negative"},
		{name: "reorder exceeds timeout", mutate: func(c *Config) { c.ReorderWindow = c.TurnTimeout }, wantError: "reorder_window must be less than turn_timeout"},
		{name: "active turn bound", mutate: func(c *Config) { c.MaxActiveTurns = 0 }, wantError: "max_active_turns must be positive"},
		{name: "event bound", mutate: func(c *Config) { c.MaxEvents = 0 }, wantError: "max_events_per_turn must be positive"},
		{name: "signals default", mutate: func(*Config) {}},
		{name: "signals logs", mutate: func(c *Config) { c.Signals = []string{SignalLogs} }},
		{name: "signals traces", mutate: func(c *Config) { c.Signals = []string{SignalTraces} }},
		{name: "signals both", mutate: func(c *Config) { c.Signals = []string{SignalLogs, SignalTraces} }},
		{name: "signals empty", mutate: func(c *Config) { c.Signals = []string{} }, wantError: "signals must list at least one of logs, traces"},
		{name: "signals unknown", mutate: func(c *Config) { c.Signals = []string{"metrics"} }, wantError: `signals: unknown signal "metrics"`},
		{name: "signals duplicate", mutate: func(c *Config) { c.Signals = []string{SignalLogs, SignalLogs} }, wantError: `signals: "logs" listed twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewDefaultConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}
