package bg

import "testing"

func TestProbeRecoveredWhenSupplierDirectProbeSucceeds(t *testing.T) {
	if !probeRecovered(nodeProbeRoundResult{ok: true}) {
		t.Fatal("direct supplier success must recover routing")
	}
}

func TestProbeRecoveredWhenSupplierDirectProbeFails(t *testing.T) {
	if probeRecovered(nodeProbeRoundResult{ok: false}) {
		t.Fatal("failed supplier direct probe must not recover routing")
	}
}
