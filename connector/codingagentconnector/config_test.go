package codingagentconnector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/confmap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantError string
	}{
		{name: "defaults", mutate: func(*Config) {}},
		{name: "codex turn timeout", mutate: func(c *Config) { c.Codex.TurnTimeout = 0 }, wantError: "codex: turn_timeout must be positive"},
		{name: "codex reorder negative", mutate: func(c *Config) { c.Codex.ReorderWindow = -time.Second }, wantError: "codex: reorder_window"},
		{name: "codex reorder exceeds timeout", mutate: func(c *Config) { c.Codex.ReorderWindow = c.Codex.TurnTimeout }, wantError: "codex: reorder_window must be less than"},
		{name: "codex active turn bound", mutate: func(c *Config) { c.Codex.MaxActiveTurns = 0 }, wantError: "codex: max_active_turns must be positive"},
		{name: "codex event bound", mutate: func(c *Config) { c.Codex.MaxEvents = 0 }, wantError: "codex: max_events_per_turn must be positive"},
		{name: "cursor turn timeout", mutate: func(c *Config) { c.Cursor.TurnTimeout = 0 }, wantError: "cursor: turn_timeout must be positive"},
		{name: "cursor reorder negative", mutate: func(c *Config) { c.Cursor.ReorderWindow = -time.Second }, wantError: "cursor: reorder_window"},
		{name: "cursor reorder exceeds timeout", mutate: func(c *Config) { c.Cursor.ReorderWindow = c.Cursor.TurnTimeout }, wantError: "cursor: reorder_window must be less than"},
		{name: "cursor active turn bound", mutate: func(c *Config) { c.Cursor.MaxActiveTurns = 0 }, wantError: "cursor: max_active_turns must be positive"},
		{name: "cursor event bound", mutate: func(c *Config) { c.Cursor.MaxEvents = 0 }, wantError: "cursor: max_events_per_turn must be positive"},
		{name: "codex signals empty", mutate: func(c *Config) { c.Codex.Signals = nil }, wantError: "codex: signals must list at least one"},
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

// TestConfigValidateNamesTheFailingBlock pins that a sub-config error reaches
// the operator with the YAML key it came from, since both blocks accept the
// same key names.
func TestConfigValidateNamesTheFailingBlock(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.Cursor.MaxEvents = 0
	require.ErrorContains(t, cfg.Validate(), "cursor: max_events_per_turn must be positive")
}

func TestDefaultConfigSignalsCodexLogs(t *testing.T) {
	cfg := createDefaultConfig()
	require.Equal(t, []string{codex.SignalLogs}, cfg.Codex.Signals)
	require.True(t, cfg.Codex.SignalEnabled(codex.SignalLogs))
	require.False(t, cfg.Codex.SignalEnabled(codex.SignalTraces))
}

func TestDefaultConfigMatchesSubConfigDefaults(t *testing.T) {
	cfg := createDefaultConfig()
	require.Equal(t, codex.NewDefaultConfig(), cfg.Codex)
	require.Equal(t, cursor.NewDefaultConfig(), cfg.Cursor)
}

// TestConfigDecodeCodexSignalsReplacesDefault pins that a configured list
// replaces the default rather than merging with it: `codex: {signals:
// [traces]}` must yield exactly [traces], not [logs traces].
func TestConfigDecodeCodexSignalsReplacesDefault(t *testing.T) {
	cfg := createDefaultConfig()
	conf := confmap.NewFromStringMap(map[string]any{"codex": map[string]any{"signals": []any{"traces"}}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, []string{codex.SignalTraces}, cfg.Codex.Signals)
	require.True(t, cfg.Codex.SignalEnabled(codex.SignalTraces))
	require.False(t, cfg.Codex.SignalEnabled(codex.SignalLogs))
	require.NoError(t, cfg.Validate())

	// Both values together replace too.
	cfg = createDefaultConfig()
	conf = confmap.NewFromStringMap(map[string]any{"codex": map[string]any{"signals": []any{"logs", "traces"}}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, []string{codex.SignalLogs, codex.SignalTraces}, cfg.Codex.Signals)
	require.NoError(t, cfg.Validate())
}

// TestConfigDecodeNestedBlocks pins the YAML shape: each harness's tuning keys
// live under its own block, and a block left out keeps that harness's defaults.
func TestConfigDecodeNestedBlocks(t *testing.T) {
	cfg := createDefaultConfig()
	conf := confmap.NewFromStringMap(map[string]any{
		"codex":       map[string]any{"turn_timeout": "30s", "reorder_window": "2s", "max_active_turns": 5, "max_events_per_turn": 7},
		"cursor":      map[string]any{"max_events_per_turn": 9},
		"claude_code": map[string]any{"chat_source": "logs"},
	})
	require.NoError(t, conf.Unmarshal(cfg))

	wantCodex := codex.NewDefaultConfig()
	wantCodex.TurnTimeout = 30 * time.Second
	wantCodex.ReorderWindow = 2 * time.Second
	wantCodex.MaxActiveTurns = 5
	wantCodex.MaxEvents = 7
	require.Equal(t, wantCodex, cfg.Codex, "a partial block must keep the edge's other defaults, including signals")
	require.Equal(t, cursor.Config{
		TurnTimeout:    10 * time.Minute,
		ReorderWindow:  30 * time.Second,
		MaxActiveTurns: 10_000,
		MaxEvents:      9,
	}, cfg.Cursor, "a partial block must keep the edge's other defaults")
	require.Equal(t, claude.Config{ChatSource: claude.ChatLogs}, cfg.ClaudeCode)
	require.True(t, cfg.ClaudeCode.ChatFromLogs())
}

// TestConfigDecodeRejectsUnnestedTuningKeys pins that the moved keys are
// rejected at the top level instead of silently ignored: confmap decodes
// strictly, so an operator following the pre-split YAML gets an error.
func TestConfigDecodeRejectsUnnestedTuningKeys(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		key   string
	}{
		{name: "top-level turn_timeout", input: map[string]any{"turn_timeout": "10m"}, key: "turn_timeout"},
		{name: "connector-wide key inside codex", input: map[string]any{"codex": map[string]any{"capture_identity": false}}, key: "capture_identity"},
		{name: "unnested claude chat source", input: map[string]any{"chat_source": "logs"}, key: "chat_source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := confmap.NewFromStringMap(tt.input).Unmarshal(createDefaultConfig())
			require.ErrorContains(t, err, "invalid keys")
			require.ErrorContains(t, err, tt.key)
		})
	}
}

// TestConfigValidateNamesClaudeCodeBlock pins that a claude_code sub-config
// error reaches the operator with the YAML key it came from.
func TestConfigValidateNamesClaudeCodeBlock(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.ClaudeCode.ChatSource = "both"
	require.ErrorContains(t, cfg.Validate(), `claude_code: chat_source: unknown value "both"`)
}

func TestFactoryDefaultsAreIndependent(t *testing.T) {
	factory := NewFactory()
	first := factory.CreateDefaultConfig().(*Config)
	second := factory.CreateDefaultConfig().(*Config)
	first.Codex.MaxEvents = 1
	first.Codex.Signals[0] = codex.SignalTraces
	require.Equal(t, 1000, second.Codex.MaxEvents)
	require.Equal(t, []string{codex.SignalLogs}, second.Codex.Signals, "each default config owns its signals slice")
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
