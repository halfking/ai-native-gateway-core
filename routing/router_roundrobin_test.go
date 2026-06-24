package routing

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
)

// TestPlanCandidates_RoundRobinEqualScores verifies that when multiple
// candidates have equal routing scores, they are distributed evenly
// via round-robin rotation instead of always selecting the first one.
func TestPlanCandidates_RoundRobinEqualScores(t *testing.T) {
	r := &Router{}

	// Create 2 candidates with identical tier, priority, and cost
	// (this simulates equal routing_score in real scenarios)
	// Set Routable=true and BlockReason=nil to make them available
	candidates := []provider.Candidate{
		{
			CredentialID: 100,
			ProviderID:   1,
			Tier:         1,
			RawModel:     "mock-model",
			BaseURL:      "http://localhost:8901",
			Routable:     true, // Must be routable
			BlockReason:  nil,  // No block reason
		},
		{
			CredentialID: 200,
			ProviderID:   1,
			Tier:         1,
			RawModel:     "mock-model",
			BaseURL:      "http://localhost:8902",
			Routable:     true, // Must be routable
			BlockReason:  nil,  // No block reason
		},
	}

	policy := provider.DefaultPolicy()

	// Run 1000 iterations to collect distribution
	distribution := make(map[int]int)
	for i := 0; i < 1000; i++ {
		planned := r.PlanCandidates(candidates, nil, policy, nil)
		assert.NotEmpty(t, planned, "should return at least one candidate")

		// Record which credential was selected first
		firstCredID := planned[0].CredentialID
		distribution[firstCredID]++
	}

	// Both credentials should be selected roughly 50% of the time
	// Allow 10% variance (400-600 out of 1000)
	cred100Count := distribution[100]
	cred200Count := distribution[200]

	t.Logf("Distribution over 1000 requests: cred100=%d (%.1f%%), cred200=%d (%.1f%%)",
		cred100Count, float64(cred100Count)/10,
		cred200Count, float64(cred200Count)/10)

	assert.GreaterOrEqual(t, cred100Count, 400, "credential 100 should get at least 40% of requests")
	assert.LessOrEqual(t, cred100Count, 600, "credential 100 should get at most 60% of requests")

	assert.GreaterOrEqual(t, cred200Count, 400, "credential 200 should get at least 40% of requests")
	assert.LessOrEqual(t, cred200Count, 600, "credential 200 should get at most 60% of requests")

	// Verify total is correct
	assert.Equal(t, 1000, cred100Count+cred200Count, "total should be 1000")
}

// TestPlanCandidates_RoundRobinThreeCandidates tests load balancing with 3 equal candidates
func TestPlanCandidates_RoundRobinThreeCandidates(t *testing.T) {
	r := &Router{}

	candidates := []provider.Candidate{
		{CredentialID: 1, Tier: 1, Routable: true, BlockReason: nil},
		{CredentialID: 2, Tier: 1, Routable: true, BlockReason: nil},
		{CredentialID: 3, Tier: 1, Routable: true, BlockReason: nil},
	}

	policy := provider.DefaultPolicy()
	distribution := make(map[int]int)

	for i := 0; i < 1500; i++ {
		planned := r.PlanCandidates(candidates, nil, policy, nil)
		distribution[planned[0].CredentialID]++
	}

	// Each should get roughly 33% (1500/3 = 500)
	// Allow 15% variance (350-650)
	for credID, count := range distribution {
		t.Logf("Credential %d: %d requests (%.1f%%)", credID, count, float64(count)/15)
		assert.GreaterOrEqual(t, count, 350, "credential %d should get at least 23%% of requests", credID)
		assert.LessOrEqual(t, count, 650, "credential %d should get at most 43%% of requests", credID)
	}

	assert.Len(t, distribution, 3, "all 3 credentials should be used")
}

// TestPlanCandidates_DifferentTiers verifies that tier-based priority is preserved
// while still applying round-robin within each tier
func TestPlanCandidates_DifferentTiers(t *testing.T) {
	r := &Router{}

	candidates := []provider.Candidate{
		{CredentialID: 10, Tier: 1, Routable: true, BlockReason: nil}, // Tier 1 (highest)
		{CredentialID: 11, Tier: 1, Routable: true, BlockReason: nil},
		{CredentialID: 20, Tier: 2, Routable: true, BlockReason: nil}, // Tier 2 (lower)
		{CredentialID: 21, Tier: 2, Routable: true, BlockReason: nil},
	}

	policy := provider.DefaultPolicy()
	tierDistribution := make(map[int]int)
	tier1Distribution := make(map[int]int)

	for i := 0; i < 1000; i++ {
		planned := r.PlanCandidates(candidates, nil, policy, nil)
		firstCred := planned[0].CredentialID

		if firstCred == 10 || firstCred == 11 {
			tierDistribution[1]++
			tier1Distribution[firstCred]++
		} else {
			tierDistribution[2]++
		}
	}

	// Tier 1 should always be selected first (100%)
	assert.Equal(t, 1000, tierDistribution[1], "tier 1 should always be selected")
	assert.Equal(t, 0, tierDistribution[2], "tier 2 should never be selected first")

	// Within tier 1, both credentials should be balanced
	t.Logf("Tier 1 distribution: cred10=%d, cred11=%d", tier1Distribution[10], tier1Distribution[11])
	assert.GreaterOrEqual(t, tier1Distribution[10], 400, "tier 1 credential 10 should get ~40-60%")
	assert.LessOrEqual(t, tier1Distribution[10], 600, "tier 1 credential 10 should get ~40-60%")
}

// TestRotateCandidates tests the rotate function
func TestRotateCandidates(t *testing.T) {
	candidates := []provider.Candidate{
		{CredentialID: 1, Routable: true, BlockReason: nil},
		{CredentialID: 2, Routable: true, BlockReason: nil},
		{CredentialID: 3, Routable: true, BlockReason: nil},
	}

	// Rotate by 0 - should return unchanged
	rotated := rotateCandidates(candidates, 0)
	assert.Equal(t, []int{1, 2, 3}, extractCredIDs(rotated))

	// Rotate by 1
	rotated = rotateCandidates(candidates, 1)
	assert.Equal(t, []int{2, 3, 1}, extractCredIDs(rotated))

	// Rotate by 2
	rotated = rotateCandidates(candidates, 2)
	assert.Equal(t, []int{3, 1, 2}, extractCredIDs(rotated))

	// Rotate by 3 (full cycle)
	rotated = rotateCandidates(candidates, 3)
	assert.Equal(t, []int{1, 2, 3}, extractCredIDs(rotated))

	// Rotate by 4 (overflow)
	rotated = rotateCandidates(candidates, 4)
	assert.Equal(t, []int{2, 3, 1}, extractCredIDs(rotated))
}

// Helper function to extract credential IDs
func extractCredIDs(cands []provider.Candidate) []int {
	ids := make([]int, len(cands))
	for i, c := range cands {
		ids[i] = c.CredentialID
	}
	return ids
}
