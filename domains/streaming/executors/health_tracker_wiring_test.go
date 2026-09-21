package executors

import (
	"os"
	"strings"
	"testing"
)

// TestDispatchDeferWiresHealthTrackerOnError guards the 2026-09-08 re-wiring
// of the credential-health feedback loop. The legacy sync candidate loop used
// to call HealthTracker.OnError; f7eb0eb1b retired that loop and the failure
// side went dark — the 80% hard-degrade, kind-gradient thresholds, probe
// gating, concurrency auto-tune and the admin sliding window all silently
// stopped seeing failures while OnSuccess stayed wired. A source-contract
// test is deliberate here: the regression mode was an innocent-looking
// refactor, not a behavior change any unit test could catch.
func TestDispatchDeferWiresHealthTrackerOnError(t *testing.T) {
	src, err := os.ReadFile("executor_dispatch.go")
	if err != nil {
		t.Fatalf("read executor_dispatch.go: %v", err)
	}
	if !strings.Contains(string(src), "HealthTracker.OnError(") {
		t.Fatal("executor_dispatch.go no longer wires HealthTracker.OnError in the dispatch failure defer — " +
			"the credential-health degradation loop and the admin sliding window need the failure samples. " +
			"If the failure funnel moved, re-attach OnError at the new single funnel point.")
	}
	if !strings.Contains(string(src), "HealthTracker.OnSuccess(") {
		t.Fatal("executor_dispatch.go no longer wires HealthTracker.OnSuccess")
	}
}
