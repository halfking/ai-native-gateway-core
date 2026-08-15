package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
)

// TestEstimatePromptTokens_UsesBodyBytes verifies the tpm-pacing estimate now
// reflects body size (chars/3.5) instead of the old constant 0, while an
// empty/nil body still returns 0 so the tpm governor keeps its conservative
// defaultTokenEstimate fallback.
func TestEstimatePromptTokens_UsesBodyBytes(t *testing.T) {
	assert.Equal(t, 0, estimatePromptTokens(nil))
	assert.Equal(t, 0, estimatePromptTokens(&ExecParams{}))

	// 350 chars / 3.5 = 100 tokens.
	p := &ExecParams{BodyBytes: make([]byte, 350)}
	assert.Equal(t, 100, estimatePromptTokens(p))

	// 700 chars / 3.5 = 200 tokens (proves it scales with size).
	p.BodyBytes = make([]byte, 700)
	assert.Equal(t, 200, estimatePromptTokens(p))
}

// TestCapacityPenaltyForWeight verifies the saturating mapping: default/larger
// weight → 0 penalty; sub-default weight rises linearly toward 1.
func TestCapacityPenaltyForWeight(t *testing.T) {
	assert.Equal(t, 0.0, capacityPenaltyForWeight(0))    // unknown treated as default → 0
	assert.Equal(t, 0.0, capacityPenaltyForWeight(-5))   // negative treated as default → 0
	assert.Equal(t, 0.0, capacityPenaltyForWeight(100))  // default → 0
	assert.Equal(t, 0.0, capacityPenaltyForWeight(1000)) // larger → 0 (saturated)
	assert.InDelta(t, 0.5, capacityPenaltyForWeight(50), 1e-9)
	assert.InDelta(t, 0.9, capacityPenaltyForWeight(10), 1e-9)
	assert.InDelta(t, 0.99, capacityPenaltyForWeight(1), 1e-9) // 1 - 1/100
}

// TestPickWeightedTie_CapacityProportional verifies the equal-load tie-break is
// capacity-proportional: a 10:90 weight pair picks the heavier side ~90% of the
// time, while equal weights stay ~50/50 (the original anti-bias preserved).
func TestPickWeightedTie_CapacityProportional(t *testing.T) {
	light := provider.Candidate{CredentialID: 1, Weight: 10}
	heavy := provider.Candidate{CredentialID: 2, Weight: 90}

	const iters = 4000
	heavyWins := 0
	for i := 0; i < iters; i++ {
		if pickWeightedTie(light, heavy).CredentialID == heavy.CredentialID {
			heavyWins++
		}
	}
	ratio := float64(heavyWins) / float64(iters)
	assert.GreaterOrEqual(t, ratio, 0.83) // expect ~0.9, generous RNG tolerance
	assert.LessOrEqual(t, ratio, 0.97)

	// Equal weights → ~50/50.
	a := provider.Candidate{CredentialID: 1, Weight: 100}
	b := provider.Candidate{CredentialID: 2, Weight: 100}
	aWins := 0
	for i := 0; i < iters; i++ {
		if pickWeightedTie(a, b).CredentialID == a.CredentialID {
			aWins++
		}
	}
	ratioA := float64(aWins) / float64(iters)
	assert.GreaterOrEqual(t, ratioA, 0.43)
	assert.LessOrEqual(t, ratioA, 0.57)

	// Both zero/unknown → still ~50/50, no panic.
	x := provider.Candidate{CredentialID: 7, Weight: 0}
	y := provider.Candidate{CredentialID: 8, Weight: 0}
	xWins := 0
	for i := 0; i < iters; i++ {
		if pickWeightedTie(x, y).CredentialID == x.CredentialID {
			xWins++
		}
	}
	ratioX := float64(xWins) / float64(iters)
	assert.GreaterOrEqual(t, ratioX, 0.43)
	assert.LessOrEqual(t, ratioX, 0.57)
}
