// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claude

import "fmt"

const (
	// ChatTraces and ChatLogs are the values chat_source accepts. The empty
	// value decodes as ChatTraces.
	ChatTraces = "traces"
	ChatLogs   = "logs"
)

// Config controls which signal emits canonical Claude Code chat spans. It maps
// to the connector's `claude_code:` block.
type Config struct {
	// ChatSource picks the signal that emits canonical Claude Code chat
	// spans: "traces" (the default) keeps the native
	// claude_code.llm_request spans as the chat source; "logs" drops those
	// spans and synthesizes chat spans from claude_code.api_request /
	// claude_code.api_error log records instead. Exactly one source emits
	// chat, so canonical output never duplicates a request. A deployment that
	// splits Claude Code logs and traces across two connector instances must
	// set the same value on both.
	ChatSource string `mapstructure:"chat_source"`
}

// NewDefaultConfig returns an independent default configuration.
func NewDefaultConfig() Config {
	return Config{ChatSource: ChatTraces}
}

// Validate rejects an unknown chat source. The empty value decodes as traces.
func (c Config) Validate() error {
	switch c.ChatSource {
	case "", ChatTraces, ChatLogs:
		return nil
	}
	return fmt.Errorf("chat_source: unknown value %q (want traces or logs)", c.ChatSource)
}

// ChatFromLogs reports whether canonical Claude Code chat spans come from the
// logs signal instead of the native traces signal.
func (c Config) ChatFromLogs() bool { return c.ChatSource == ChatLogs }
