package reasoncap

import "testing"

// TestGrok46Config verifies grok-4.6 reasoning capabilities configuration.
func TestGrok46Config(t *testing.T) {
	caps, ok := inferFromName("grok-4.6")
	if !ok {
		t.Fatal("grok-4.6 should match")
	}

	if !caps.Supported {
		t.Error("grok-4.6 should support reasoning")
	}

	if caps.Dialect != DialectGrok {
		t.Errorf("grok-4.6 dialect = %q, want %q", caps.Dialect, DialectGrok)
	}

	if !caps.CanDisable {
		t.Error("grok-4.6 should be able to disable reasoning")
	}

	// Verify effort levels: low, medium, high, xhigh
	expectedEfforts := []string{"low", "medium", "high", "xhigh"}
	if len(caps.Efforts) != len(expectedEfforts) {
		t.Errorf("grok-4.6 efforts count = %d, want %d", len(caps.Efforts), len(expectedEfforts))
	}

	effortMap := make(map[string]bool)
	for _, e := range caps.Efforts {
		effortMap[e] = true
	}

	for _, expected := range expectedEfforts {
		if !effortMap[expected] {
			t.Errorf("grok-4.6 missing effort level: %q", expected)
		}
	}

	// Verify no budget-based reasoning (uses effort enum instead)
	if caps.BudgetMin != 0 || caps.BudgetMax != 0 {
		t.Errorf("grok-4.6 should not use budget-based reasoning, got min=%d max=%d",
			caps.BudgetMin, caps.BudgetMax)
	}
}
