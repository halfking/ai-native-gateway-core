package sessionv2mirror

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

// Pairs with PersistHook_ProbeWithRealSessionLaneAware: canary eligibility
// must use the same ShouldIsolate gate so data-horizon cannot false-stale.
func TestEligibleForTurnMirrorPolicy_SessionHeadedProbeLaneAware(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		RequestID:   "req-probe-with-session",
		Success:     true,
		GwSessionID: strPtr("gw_real_session"),
		OriginStage: strPtr("node_probe"),
		TaskType:    strPtr("probe_triggered"),
	}

	probelane.SetPhysicalEnabledForTest(false)
	defer probelane.ResetPhysicalOverrideForTest()
	if !EligibleForTurnMirrorPolicy(entry) {
		t.Fatal("physical off: session-headed probe must remain canary-eligible (legacy mirror path)")
	}

	probelane.SetPhysicalEnabledForTest(true)
	if EligibleForTurnMirrorPolicy(entry) {
		t.Fatal("physical on: session-headed probe must NOT be canary-eligible")
	}
}
