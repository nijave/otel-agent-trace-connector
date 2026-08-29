//go:build e2e

package validator

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TestRoutingAffinity checks the two-tier routing e2e: every synthetic
// conversation replayed through the gateway must land whole (root + chat) on
// exactly one tier-2 replica. Spread across replicas is logged, not asserted:
// with a consistent hash, all conversations legally landing on one backend is
// improbable but valid.
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

	// Turns finalize via the 30s inactivity timeout after the replay stops,
	// then the file exporter flushes; poll well past both.
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = checkAffinity(pathA, pathB, runID, count)
		if lastErr == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("routing affinity not satisfied: %v", lastErr)
}

func checkAffinity(pathA, pathB, runID string, count int) error {
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
	return nil
}

func fileRoutingHomes(path, runID string) (map[string]routingHome, error) {
	var homes map[string]routingHome
	err := validateTraceFile(path, runID, func(traces ptrace.Traces, runID string) error {
		homes = routingHomes(traces, runID)
		return nil
	})
	return homes, err
}
