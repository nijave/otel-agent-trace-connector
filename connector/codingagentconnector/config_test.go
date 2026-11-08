package codingagentconnector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/confmap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
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

func TestDefaultConfigSignalsCodexLogs(t *testing.T) {
	cfg := createDefaultConfig()
	require.Equal(t, map[string][]string{codex.HarnessCodex: {codex.SignalLogs}}, cfg.Signals)
	require.True(t, cfg.SignalEnabled(codex.HarnessCodex, codex.SignalLogs))
	require.False(t, cfg.SignalEnabled(codex.HarnessCodex, codex.SignalTraces))
	// Harnesses outside the map are always constructed; the helper answers only
	// about the map.
	require.False(t, cfg.SignalEnabled("cursor", codex.SignalLogs))
}

func TestConfigValidateSignals(t *testing.T) {
	tests := []struct {
		name      string
		signals   map[string][]string
		wantError string
	}{
		{name: "codex logs", signals: map[string][]string{"codex": {"logs"}}},
		{name: "codex traces", signals: map[string][]string{"codex": {"traces"}}},
		{name: "codex both", signals: map[string][]string{"codex": {"logs", "traces"}}},
		{name: "empty map", signals: map[string][]string{}, wantError: "signals"},
		{name: "empty list", signals: map[string][]string{"codex": {}}, wantError: "at least one signal"},
		{name: "unknown harness", signals: map[string][]string{"cursor": {"logs"}}, wantError: `unknown harness "cursor"`},
		{name: "unknown signal", signals: map[string][]string{"codex": {"metrics"}}, wantError: `"metrics"`},
		{name: "duplicate signal", signals: map[string][]string{"codex": {"logs", "logs"}}, wantError: "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createDefaultConfig()
			cfg.Signals = tt.signals
			err := cfg.Validate()
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

// TestConfigDecodeSignalsReplacesDefault pins that a configured map replaces
// the harness's default list rather than merging with it: `signals:
// {codex: [traces]}` must yield exactly [traces], not [logs traces].
func TestConfigDecodeSignalsReplacesDefault(t *testing.T) {
	cfg := createDefaultConfig()
	conf := confmap.NewFromStringMap(map[string]any{"signals": map[string]any{"codex": []any{"traces"}}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, map[string][]string{codex.HarnessCodex: {codex.SignalTraces}}, cfg.Signals)
	require.True(t, cfg.SignalEnabled(codex.HarnessCodex, codex.SignalTraces))
	require.False(t, cfg.SignalEnabled(codex.HarnessCodex, codex.SignalLogs))
	require.NoError(t, cfg.Validate())

	// Both values together replace too.
	cfg = createDefaultConfig()
	conf = confmap.NewFromStringMap(map[string]any{"signals": map[string]any{"codex": []any{"logs", "traces"}}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, map[string][]string{codex.HarnessCodex: {codex.SignalLogs, codex.SignalTraces}}, cfg.Signals)
	require.NoError(t, cfg.Validate())
}

func TestConfigValidateClaudeChatSource(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantError string
	}{
		{name: "default", value: codex.ClaudeChatTraces},
		{name: "logs", value: codex.ClaudeChatLogs},
		{name: "unset decodes as traces", value: ""},
		{name: "unknown", value: "both", wantError: `claude_chat_source: unknown value "both"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createDefaultConfig()
			cfg.ClaudeChatSource = tt.value
			err := cfg.Validate()
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

// TestConfigDecodeClaudeChatSource pins the field through the same
// mapstructure decoding OCB uses.
func TestConfigDecodeClaudeChatSource(t *testing.T) {
	cfg := createDefaultConfig()
	require.Equal(t, codex.ClaudeChatTraces, cfg.ClaudeChatSource, "default must be traces")
	require.False(t, cfg.ClaudeChatsFromLogs())

	conf := confmap.NewFromStringMap(map[string]any{"claude_chat_source": "logs"})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, codex.ClaudeChatLogs, cfg.ClaudeChatSource)
	require.True(t, cfg.ClaudeChatsFromLogs())
	require.NoError(t, cfg.Validate())
}

func TestFactoryDefaultsAreIndependent(t *testing.T) {
	factory := NewFactory()
	first := factory.CreateDefaultConfig().(*Config)
	second := factory.CreateDefaultConfig().(*Config)
	first.MaxEvents = 1
	require.Equal(t, 1000, second.MaxEvents)
	require.Equal(t, componentType, factory.Type())
}

func TestDefaultConfigCapturesIdentity(t *testing.T) {
	cfg := createDefaultConfig()
	if !cfg.CaptureIdentity {
		t.Fatal("capture_identity must default to true")
	}
}

// TestConfigDecodeCaptureIdentityFalse complements the default-true test:
// capture_identity must also be settable to false through the same
// mapstructure decoding OCB uses, not just constructible in Go.
func TestConfigDecodeCaptureIdentityFalse(t *testing.T) {
	cfg := createDefaultConfig()
	require.True(t, cfg.CaptureIdentity, "precondition: default must start true")

	conf := confmap.NewFromStringMap(map[string]any{"capture_identity": false})
	require.NoError(t, conf.Unmarshal(cfg))
	require.False(t, cfg.CaptureIdentity)
}
