package routing

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/health"
	"github.com/stretchr/testify/assert"
)

// TestWeightedRouter_ExcludesUnhealthy verifies that credentials marked Unhealthy
// by the ErrorDetector are completely excluded from routing (weight = 0).
//
// This test was added after the local integration test discovered a bug:
// WeightedRouter was not reading the IsUnhealthy state from ErrorDetector,
// so credentials marked Unhealthy still received traffic.
func TestWeightedRouter_ExcludesUnhealthy(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(3)

	wr.RegisterWithDetector(NewCandidateFromID("a-good"), det)
	wr.RegisterWithDetector(NewCandidateFromID("a-bad"), health.NewErrorDetector(3))

	// T0: Both healthy
	assert.Equal(t, 1.0, wr.Weight("a-good"))
	assert.Equal(t, 1.0, wr.Weight("a-bad"))

	// T1: Send 3 consecutive errors to a-bad
	for i := 0; i < 3; i++ {
		det.OnError(health.ErrorEvent{
			CredentialID: "a-bad",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
		wr.RecordError("a-bad", 500, nil)
	}

	time.Sleep(1100 * time.Millisecond) // expire weight cache

	// a-bad should be marked Unhealthy and have weight 0
	assert.True(t, det.IsUnhealthy("a-bad"), "a-bad should be marked Unhealthy")
	assert.Equal(t, 0.0, wr.Weight("a-bad"), "a-bad should have weight 0 (excluded from routing)")

	// a-good should still be healthy with full weight
	assert.Equal(t, 1.0, wr.Weight("a-good"), "a-good should still be healthy")

	// Verify a-bad is never selected
	const selections = 1000
	selected := map[string]int{}
	for i := 0; i < selections; i++ {
		c := wr.SelectWeighted()
		if c != nil {
			selected[c.CredentialID]++
		}
	}

	t.Logf("Selection distribution after a-bad marked Unhealthy:")
	for id, count := range selected {
		t.Logf("  %s: %d", id, count)
	}
	assert.Equal(t, 0, selected["a-bad"], "a-bad should never be selected when Unhealthy")
	assert.Equal(t, selections, selected["a-good"], "a-good should receive all traffic")
}

// TestWeightedRouter_UnhealthyRecovery verifies that a credential recovers
// from Unhealthy state when consecutive failures are reset.
func TestWeightedRouter_UnhealthyRecovery(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(3)

	wr.RegisterWithDetector(NewCandidateFromID("recovering"), det)
	wr.RegisterWithDetector(NewCandidateFromID("healthy"), health.NewErrorDetector(3))

	// T0: Trigger 3 failures on "recovering"
	for i := 0; i < 3; i++ {
		det.OnError(health.ErrorEvent{
			CredentialID: "recovering",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
		wr.RecordError("recovering", 500, nil)
	}

	time.Sleep(1100 * time.Millisecond)
	assert.Equal(t, 0.0, wr.Weight("recovering"), "Should be Unhealthy (weight 0)")
	assert.True(t, det.IsUnhealthy("recovering"))

	// T1: Reset failures (e.g., admin action)
	det.ResetFailures("recovering")

	// ResetFailures doesn't reduce the weight penalty since the sliding window
	// still has errors. To fully recover, we need to clear both.
	// Simulate 10 successful requests which will reset consecutive fails
	for i := 0; i < 10; i++ {
		wr.RecordSuccess("recovering", 50*time.Millisecond)
	}

	time.Sleep(1100 * time.Millisecond)

	// After successes, consecutiveFails should be 0 (each success reduces by 1)
	assert.False(t, det.IsUnhealthy("recovering"), "Should no longer be Unhealthy")
	// Weight should be > 0 (still has some errors in sliding window)
	weight := wr.Weight("recovering")
	t.Logf("Recovered weight: %v", weight)
	assert.Greater(t, weight, 0.0, "Recovered credential should have non-zero weight")
}
