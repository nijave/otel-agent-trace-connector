//go:build e2e

package validator

import (
	"crypto/md5"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TestRoutingAffinity checks the two-tier routing e2e: every synthetic
// conversation replayed through the gateway must land whole (root + chat) on
// exactly one tier-2 replica, and every synthetic Claude Code trace replayed
// with its own trace ID must land whole on exactly one replica, with both
// replicas receiving Claude traces (the traceID route spreads stateless
// traffic). Spread of the conversations across replicas is logged, not
// asserted: with a consistent hash, all conversations legally landing on one
// backend is improbable but valid.
func TestRoutingAffinity(t *testing.T) {
	// E2E_RUN_ID, CANONICAL_FILE_A, and CANONICAL_FILE_B are the signals only
	// scripts/e2e-routing.sh sets; any one missing means no routing run
	// happened, so the test skips rather than fails. This must not regress to
	// t.Fatal on a partial signal: every other e2e script's shared harness
	// (scripts/lib-e2e.sh) sets E2E_RUN_ID and runs the whole package
	// untargeted (`go test -tags=e2e ./e2e/validator/`) without ever setting
	// CANONICAL_FILE_A/B, so treating E2E_RUN_ID alone as "a routing run is
	// underway" would fail this test during every other agent's paid e2e run.
	runID := os.Getenv("E2E_RUN_ID")
	pathA := os.Getenv("CANONICAL_FILE_A")
	pathB := os.Getenv("CANONICAL_FILE_B")
	if runID == "" || pathA == "" || pathB == "" {
		t.Skip("CANONICAL_FILE_A, CANONICAL_FILE_B, and E2E_RUN_ID must be set; run scripts/e2e-routing.sh")
	}
	count, err := strconv.Atoi(os.Getenv("CONVERSATIONS"))
	if err != nil || count < 1 {
		t.Fatal("CONVERSATIONS must be a positive integer")
	}
	claudeCount, err := strconv.Atoi(os.Getenv("CLAUDE_TRACES"))
	if err != nil || claudeCount < 1 {
		t.Fatal("CLAUDE_TRACES must be a positive integer")
	}

	// Turns finalize via the 30s inactivity timeout after the replay stops,
	// then the file exporter flushes; poll well past both.
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = checkAffinity(pathA, pathB, runID, count, claudeCount)
		if lastErr == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("routing affinity not satisfied: %v", lastErr)
}

func checkAffinity(pathA, pathB, runID string, count, claudeCount int) error {
	homesA, err := fileRoutingHomes(pathA, runID)
	if err != nil {
		return err
	}
	homesB, err := fileRoutingHomes(pathB, runID)
	if err != nil {
		return err
	}
	spreadA, spreadB := 0, 0
	for n := 1; n <= count; n++ {
		conv := fmt.Sprintf("routing-%s-%d", runID, n)
		a, inA := homesA[conv]
		b, inB := homesB[conv]
		switch {
		case inA && inB:
			return fmt.Errorf("conversation %s split across both replicas", conv)
		case !inA && !inB:
			return fmt.Errorf("conversation %s missing from both replicas", conv)
		case inA:
			if !a.HasRoot || !a.HasChat {
				return fmt.Errorf("conversation %s incomplete on replica a: %+v", conv, a)
			}
			spreadA++
		default:
			if !b.HasRoot || !b.HasChat {
				return fmt.Errorf("conversation %s incomplete on replica b: %+v", conv, b)
			}
			spreadB++
		}
	}
	fmt.Printf("routing spread: replica-a=%d replica-b=%d\n", spreadA, spreadB)
	return checkClaudeSpread(pathA, pathB, runID, claudeCount)
}

func fileRoutingHomes(path, runID string) (map[string]routingHome, error) {
	var homes map[string]routingHome
	err := validateTraceFile(path, runID, func(traces ptrace.Traces, runID string) error {
		homes = routingHomes(traces, runID)
		return nil
	})
	return homes, err
}

// claudeTraceFixtureSpans is the span count one replay of the pinned Claude
// native-traces fixture yields: every claude_code.* span passes through the
// Claude edge, none are dropped.
const claudeTraceFixtureSpans = 6

// checkClaudeSpread asserts the traceID route spreads the stateless Claude
// traffic: every replayed trace reaches exactly one replica whole, and each
// replica holds at least one trace. Trace IDs are recomputed the way
// e2e/routing/run.sh builds them, so a dropped or split trace fails instead
// of silently missing from the count.
func checkClaudeSpread(pathA, pathB, runID string, claudeCount int) error {
	spansA, err := fileClaudeTraceSpans(pathA, runID)
	if err != nil {
		return err
	}
	spansB, err := fileClaudeTraceSpans(pathB, runID)
	if err != nil {
		return err
	}
	seenA, seenB := 0, 0
	for n := 1; n <= claudeCount; n++ {
		id := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("claude-%s-%d", runID, n))))
		a, inA := spansA[id]
		b, inB := spansB[id]
		switch {
		case inA && inB:
			return fmt.Errorf("claude trace %s split across both replicas", id)
		case !inA && !inB:
			return fmt.Errorf("claude trace %s missing from both replicas", id)
		case inA:
			if a != claudeTraceFixtureSpans {
				return fmt.Errorf("claude trace %s has %d spans on replica a, want %d", id, a, claudeTraceFixtureSpans)
			}
			seenA++
		default:
			if b != claudeTraceFixtureSpans {
				return fmt.Errorf("claude trace %s has %d spans on replica b, want %d", id, b, claudeTraceFixtureSpans)
			}
			seenB++
		}
	}
	fmt.Printf("claude trace spread: replica-a=%d replica-b=%d\n", seenA, seenB)
	if seenA == 0 {
		return fmt.Errorf("no claude traces reached replica a")
	}
	if seenB == 0 {
		return fmt.Errorf("no claude traces reached replica b")
	}
	return nil
}

// fileClaudeTraceSpans maps each claude_code trace ID in the run to the
// number of canonical spans this file holds for it.
func fileClaudeTraceSpans(path, runID string) (map[string]int, error) {
	counts := map[string]int{}
	err := validateTraceFile(path, runID, func(traces ptrace.Traces, _ string) error {
		collectClaudeTraceSpans(traces, runID, counts)
		return nil
	})
	return counts, err
}

// collectClaudeTraceSpans counts, per trace ID, the canonical spans the
// Claude edge produced in this run: every claude_code.* span carries
// coding_agent.client.name=claude_code after normalization.
func collectClaudeTraceSpans(traces ptrace.Traces, runID string, counts map[string]int) {
	for _, span := range collectRunSpans(traces, runID) {
		if client, ok := span.Attributes().Get("coding_agent.client.name"); !ok || client.Str() != "claude_code" {
			continue
		}
		counts[span.TraceID().String()]++
	}
}
