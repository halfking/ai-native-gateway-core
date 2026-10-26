package settings

import "testing"

// TestOutputComplianceEnabledDefaultsOff pins the 2026-10-17 product default:
// optional output compliance is off unless explicitly enabled. Mandatory egress
// guards do not read this key.
func TestOutputComplianceEnabledDefaultsOff(t *testing.T) {
	prev := Global
	t.Cleanup(func() { Global = prev })
	Global = nil
	if GetPlatformBool("output_compliance.enabled", false) {
		t.Fatal("nil Global fallback must be false")
	}
	r := NewRegistry()
	for _, sp := range ModuleSpecs() {
		if sp.Key != "output_compliance.enabled" {
			continue
		}
		if sp.Default != false {
			t.Fatalf("ModuleSpecs Default=%v, want false", sp.Default)
		}
		if err := r.RegisterSpec(sp); err != nil {
			t.Fatal(err)
		}
	}
	Global = r
	if GetPlatformBool("output_compliance.enabled", true) {
		t.Fatal("registered default must resolve to false even if caller fallback is true")
	}
}
