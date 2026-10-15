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

	// 2026-10-13 审计轮订正：原断言 {low,medium,high,xhigh} 无实证，且与
	// 生产上游明示集冲突（vapeur grok-4.6-1 400 报错原文："Supported
	// values are: 'none', 'low', 'medium', and 'high'"，raw-logs
	// 2026-10-13T19:36）。xhigh 放行曾致 paramguard 钳制空转、整请求 400。
	expectedEfforts := []string{"none", "low", "medium", "high"}
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
