package codingagentconnector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantError string
	}{
		{name: "defaults", mutate: func(*Config) {}},
		{name: "turn timeout", mutate: func(c *Config) { c.TurnTimeout = 0 }, wantError: "turn_timeout"},
		{name: "reorder negative", mutate: func(c *Config) { c.ReorderWindow = -time.Second }, wantError: "reorder_window"},
		{name: "reorder exceeds timeout", mutate: func(c *Config) { c.ReorderWindow = c.TurnTimeout }, wantError: "less than"},
		{name: "active turn bound", mutate: func(c *Config) { c.MaxActiveTurns = 0 }, wantError: "max_active_turns"},
		{name: "event bound", mutate: func(c *Config) { c.MaxEvents = 0 }, wantError: "max_events_per_turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createDefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

func TestFactoryDefaultsAreIndependent(t *testing.T) {
	factory := NewFactory()
	first := factory.CreateDefaultConfig().(*Config)
	second := factory.CreateDefaultConfig().(*Config)
	first.MaxEvents = 1
	require.Equal(t, 1000, second.MaxEvents)
	require.Equal(t, componentType, factory.Type())
}
