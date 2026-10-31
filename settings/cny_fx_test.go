package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// cny_fx_test.go — P1-C (audit 2026-10-28 §4): the routing FX rate is
// configurable and anchored to the billing constant.

func TestCNYScoreFXDefaultMatchesBillingAnchor(t *testing.T) {
	// Default (no env): exactly the billing anchor — scoring and accounting
	// share one ruler (同尺).
	assert.Equal(t, CNYToUSDFX, CNYScoreFX())
	assert.InDelta(t, 7.2, CNYScoreFX(), 1e-12)
}

func TestCNYToUSDFXAnchor(t *testing.T) {
	// The billing anchor must stay 7.2 unless a deliberate billing change
	// lands — AssignRequestCost's arithmetic depends on it.
	assert.InDelta(t, 7.2, CNYToUSDFX, 1e-12)
}
