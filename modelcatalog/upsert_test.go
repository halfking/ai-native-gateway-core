package modelcatalog

import (
	"strings"
	"testing"
)

func TestPreserveManualDisable(t *testing.T) {
	manual := "manual"
	auto := "auto_probe_failed"
	deleted := "deleted"

	tests := []struct {
		name     string
		avail    bool
		reason   *string
		preserve bool
	}{
		{"manual disabled", false, &manual, true},
		{"manual prefix variant", false, strPtr("manual_admin"), true},
		{"deleted legacy soft clear", false, &deleted, false},
		{"auto disabled", false, &auto, false},
		{"available with reason", true, &manual, false},
		{"available no reason", true, nil, false},
		{"disabled no reason", false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreserveManualDisable(tt.avail, tt.reason)
			if got != tt.preserve {
				t.Fatalf("PreserveManualDisable(%v, %v) = %v, want %v", tt.avail, tt.reason, got, tt.preserve)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestDeriveBillingMode covers the SSOT mapping from credentials.plan_type
// to credential_model_bindings.billing_mode. This mirrors the CASE WHEN
// expression in upsertCredentialModelSQL and migrations/136.
func TestDeriveBillingMode(t *testing.T) {
	tests := []struct {
		planType string
		want     string
	}{
		{"token", "per_token"},
		{"token_plan", "token_plan"},
		{"code_plan", "code_plan"},
		{"agent_plan", "agent_plan"},
		{"monthly", "monthly"},
		{"free", "free"},
		{"", "per_token"}, // empty defaults to token semantics
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.planType, func(t *testing.T) {
			got := DeriveBillingMode(tt.planType)
			if got != tt.want {
				t.Fatalf("DeriveBillingMode(%q) = %q, want %q", tt.planType, got, tt.want)
			}
		})
	}
}

// TestUpsertSQL_DerivesBillingModeFromPlanType is a structural test guarding
// the SQL constant: the INSERT must populate billing_mode and
// plan_type_origin, and must NOT overwrite billing_mode on conflict (so
// admin/Pricing-page manual overrides survive a discovery re-fetch).
func TestUpsertSQL_DerivesBillingModeFromPlanType(t *testing.T) {
	s := upsertCredentialModelSQL

	required := []string{
		"plan_type",                          // cred CTE pulls plan_type
		"CASE WHEN cred.plan_type = 'token'", // mapping logic
		"'per_token'",                        // legacy alias
		"'auto'",                             // plan_type_origin default
		"billing_mode",                       // INSERT column
		"plan_type_origin",                   // INSERT column
	}
	for _, r := range required {
		if !strings.Contains(s, r) {
			t.Errorf("upsert SQL missing required token: %q", r)
		}
	}

	// ON CONFLICT branch must NOT touch billing_mode — manual overrides win.
	conflictIdx := strings.Index(s, "ON CONFLICT (credential_id, provider_model_id)")
	if conflictIdx < 0 {
		t.Fatal("expected ON CONFLICT clause not found in upsert SQL")
	}
	conflictBranch := s[conflictIdx:]
	if strings.Contains(conflictBranch, "billing_mode =") {
		t.Errorf("ON CONFLICT branch must not reassign billing_mode (manual overrides would be clobbered)")
	}
}
