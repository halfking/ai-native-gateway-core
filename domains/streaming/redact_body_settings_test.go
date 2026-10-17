package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

// TestRedactSettingsDefaultOffPinsProductDefault pins the 2026-10-17 product
// default: with no settings store (unit tests / settings.Global nil), optional
// output compliance stays off. redaction_mode still falls back to
// owner_mismatch for when compliance is explicitly enabled.
func TestRedactSettingsDefaultOffPinsProductDefault(t *testing.T) {
	if outputComplianceEnabled() {
		t.Fatal("output_compliance.enabled fallback must be false (optional module default off)")
	}
	if got := getRedactionMode(); got != outputcompliance.RedactOwnerMismatch {
		t.Fatalf("redaction_mode fallback = %q, want owner_mismatch", got)
	}
}
