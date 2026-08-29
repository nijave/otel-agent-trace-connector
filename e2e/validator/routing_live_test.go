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
	// E2E_RUN_ID is the primary signal scripts/e2e-routing.sh sets; matching
	// TestLiveE2ETraces's convention, its absence means "no run happened" and
	// the test skips rather than fails, so the untargeted `go test -tags=e2e
	// ./e2e/validator/` in check.sh stays green without a live stack.
	runID := os.Getenv("E2E_RUN_ID")
	if runID == "" {
		t.Skip("E2E_RUN_ID not set; run scripts/e2e-routing.sh")
	}
	pathA := os.Getenv("CANONICAL_FILE_A")
	pathB := os.Getenv("CANONICAL_FILE_B")
	if pathA == "" || pathB == "" {
		t.Fatal("CANONICAL_FILE_A and CANONICAL_FILE_B must be set")
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
