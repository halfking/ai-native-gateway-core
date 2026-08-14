package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

// TestRedactSettingsDefaultToLegacyBehavior pins the SC-2 back-compat
// invariant: with no settings store configured (unit tests / settings.Global
// nil), the previously-hardcoded values must remain the fallbacks so the
// wiring change is invisible to existing deployments.
func TestRedactSettingsDefaultToLegacyBehavior(t *testing.T) {
	if !outputComplianceEnabled() {
		t.Fatal("output_compliance.enabled fallback must stay true (legacy hardcoded value)")
	}
	if got := getRedactionMode(); got != outputcompliance.RedactOwnerMismatch {
		t.Fatalf("redaction_mode fallback = %q, want owner_mismatch (legacy hardcoded value)", got)
	}
}
