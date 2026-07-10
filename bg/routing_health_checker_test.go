package bg

import (
	"testing"
)

func TestAllHealthChecksRegistered(t *testing.T) {
	checks := AllHealthChecks()
	if len(checks) < 5 {
		t.Fatalf("expected at least 5 health checks, got %d", len(checks))
	}
	ids := map[string]bool{}
	for _, chk := range checks {
		if chk.CheckID == "" {
			t.Error("check_id must not be empty")
		}
		if chk.Severity != "critical" && chk.Severity != "warning" && chk.Severity != "info" {
			t.Errorf("check %s has invalid severity %q", chk.CheckID, chk.Severity)
		}
		if chk.Query == "" {
			t.Errorf("check %s has empty query", chk.CheckID)
		}
		if ids[chk.CheckID] {
			t.Errorf("duplicate check_id: %s", chk.CheckID)
		}
		ids[chk.CheckID] = true
	}
}

func TestHealthCheckIDs(t *testing.T) {
	expected := []string{"canonical_id_null", "billing_mismatch", "probe_missing", "family_unknown", "circuit_open"}
	checks := AllHealthChecks()
	found := map[string]bool{}
	for _, c := range checks {
		found[c.CheckID] = true
	}
	for _, id := range expected {
		if !found[id] {
			t.Errorf("missing health check: %s", id)
		}
	}
}
