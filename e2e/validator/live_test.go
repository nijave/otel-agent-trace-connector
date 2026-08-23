//go:build e2e

package validator

import (
	"os"
	"testing"
	"time"
)

// TestLiveE2ETraces validates the OTLP traces produced by a real agent run. It is
// gated behind the `e2e` build tag and driven by scripts/e2e*.sh, which set
// E2E_RUN_ID, E2E_AGENT, TRACE_FILE, and RAW_TRACE_FILE and keep the collector
// running while it polls. It skips when E2E_RUN_ID is unset.
func TestLiveE2ETraces(t *testing.T) {
	runID := os.Getenv("E2E_RUN_ID")
	if runID == "" {
		t.Skip("E2E_RUN_ID not set; run scripts/e2e.sh or scripts/e2e-claude.sh")
	}
	path := os.Getenv("TRACE_FILE")
	if path == "" {
		t.Fatal("TRACE_FILE is required")
	}
	agent := os.Getenv("E2E_AGENT")
	if agent == "" {
		agent = "codex"
	}
	switch agent {
	case "codex", "claude_code", "openai_adhoc", "strands", "opencode", "pi":
	default:
		t.Fatalf("unsupported E2E_AGENT %q", agent)
	}
	rawPath := os.Getenv("RAW_TRACE_FILE")
	if (agent == "claude_code" || agent == "strands" || agent == "opencode") && rawPath == "" {
		t.Fatal("RAW_TRACE_FILE is required for this agent's validation")
	}

	// The collector flushes the file exporter asynchronously, so poll until the
	// expected trace appears (or time out).
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = validateCanonicalFile(path, runID, agent)
		if lastErr == nil && agent == "claude_code" {
			lastErr = validateClaudeRawFile(rawPath, runID)
		}
		if lastErr == nil && agent == "strands" {
			lastErr = validateStrandsRawFile(rawPath, runID)
		}
		if lastErr == nil && agent == "opencode" {
			lastErr = validateOpenCodeRawFile(rawPath, runID)
		}
		if lastErr == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("E2E traces did not become valid: %v", lastErr)
}
