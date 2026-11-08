// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewDefaultConfig(t *testing.T) {
	require.Equal(t, Config{ChatSource: ChatTraces}, NewDefaultConfig())
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantError string
	}{
		{name: "default", value: ChatTraces},
		{name: "logs", value: ChatLogs},
		{name: "unset decodes as traces", value: ""},
		{name: "unknown", value: "both", wantError: `chat_source: unknown value "both"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{ChatSource: tt.value}
			err := cfg.Validate()
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

func TestChatFromLogs(t *testing.T) {
	require.False(t, NewDefaultConfig().ChatFromLogs())
	require.False(t, Config{ChatSource: ChatTraces}.ChatFromLogs())
	require.False(t, Config{}.ChatFromLogs(), "unset decodes as traces")
	require.True(t, Config{ChatSource: ChatLogs}.ChatFromLogs())
}
