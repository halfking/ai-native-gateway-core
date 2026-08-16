package admin

import (
	"strings"
	"testing"
)

func TestForceEnableCredentialSQLRestoresAllRoutingGates(t *testing.T) {
	for _, clause := range []string{
		"manual_disabled = false",
		"lifecycle_status = 'active'",
		"availability_state = 'ready'",
		"quota_state = 'ok'",
		"circuit_state = 'closed'",
	} {
		if !strings.Contains(forceEnableCredentialSQL, clause) {
			t.Fatalf("force-enable SQL missing %q", clause)
		}
	}
}
