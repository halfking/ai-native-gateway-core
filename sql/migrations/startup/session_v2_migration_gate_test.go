//go:build sessionv2gate

package startup

import "testing"

// TestSessionV2MigrationGate is an opt-in release gate. It intentionally fails
// while the current registered migrations do not satisfy the frozen contract.
// This keeps ordinary unit CI green while making a release decision explicit.
func TestSessionV2MigrationGate(t *testing.T) {
	contract := loadSessionV2MigrationContract(t)
	violations := detectSessionV2MigrationViolations(contract)
	if len(violations) == 0 {
		return
	}
	for _, violation := range violations {
		t.Logf("NO-GO: %s", violation)
	}
	t.Fatalf("Session V2 migration gate blocked release: %d contract violation(s)", len(violations))
}
