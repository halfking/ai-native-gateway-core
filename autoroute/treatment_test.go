package autoroute

import (
	"testing"
)

func TestDeciderSetTreatmentRolloutOverride(t *testing.T) {
	d := NewDecider(nil, nil, nil, nil)
	override := &RolloutConfig{
		Experiment:     "dispatch-v3",
		Version:        "v1",
		Enabled:        true,
		VariantPercent: 100,
		Scope:          TreatmentScopeRequest,
	}
	d.SetTreatmentRollout(override)
	got := d.treatmentConfig()
	if got.Experiment != override.Experiment || got.Version != override.Version ||
		got.Enabled != override.Enabled || got.VariantPercent != override.VariantPercent ||
		got.Scope != override.Scope {
		t.Fatalf("treatmentConfig = %+v, want override %+v", got, override)
	}
	if d.treatmentRollout == override {
		t.Fatal("SetTreatmentRollout must store a defensive copy, not the caller's pointer")
	}

	d.SetTreatmentRollout(nil)
	if d.treatmentRollout != nil {
		t.Fatal("SetTreatmentRollout(nil) must restore global configuration")
	}
}

func TestAssignTreatmentDisabledAndZeroPercentStayUnenrolled(t *testing.T) {
	cfg := RolloutConfig{
		Experiment:     "exp",
		Version:        "v1",
		Enabled:        false,
		ShadowOnly:     true,
		VariantPercent: 100,
		Scope:          TreatmentScopeTenant,
	}
	got := AssignTreatment(cfg, "tenant-1", "request-1")
	if got.Treatment != TreatmentControl || got.Experiment != "" || got.AssignmentHash != "" {
		t.Fatalf("disabled assignment = %+v, want unenrolled control", got)
	}

	cfg.Enabled = true
	cfg.VariantPercent = 0
	got = AssignTreatment(cfg, "tenant-1", "request-1")
	if got.Treatment != TreatmentControl || got.Experiment != "" {
		t.Fatalf("zero-percent assignment = %+v, want unenrolled control", got)
	}
}

func TestAssignTreatmentTenantScopeIsStableAndTenantPrecedesRequest(t *testing.T) {
	cfg := RolloutConfig{
		Experiment:     "exp",
		Version:        "v1",
		Enabled:        true,
		ShadowOnly:     true,
		VariantPercent: 100,
		Scope:          TreatmentScopeTenant,
	}
	first := AssignTreatment(cfg, "tenant-1", "request-1")
	second := AssignTreatment(cfg, "tenant-1", "request-2")
	if first != second {
		t.Fatalf("tenant assignment changed across requests: first=%+v second=%+v", first, second)
	}
	if first.Treatment != TreatmentShadow || first.Scope != TreatmentScopeTenant || first.AssignmentHash == "" {
		t.Fatalf("tenant assignment = %+v, want shadow with opaque hash", first)
	}
}

func TestAssignTreatmentRequestScopeChangesWithRequest(t *testing.T) {
	cfg := RolloutConfig{
		Experiment:     "exp",
		Version:        "v1",
		Enabled:        true,
		ShadowOnly:     false,
		VariantPercent: 100,
		Scope:          TreatmentScopeRequest,
	}
	first := AssignTreatment(cfg, "tenant-1", "request-1")
	second := AssignTreatment(cfg, "tenant-1", "request-2")
	if first.AssignmentHash == second.AssignmentHash {
		t.Fatalf("request assignments unexpectedly share hash: %+v / %+v", first, second)
	}
	if first.Treatment != TreatmentVariant || second.Treatment != TreatmentVariant {
		t.Fatalf("100%% request rollout = %+v / %+v, want variant", first, second)
	}
}

func TestAssignTreatmentMissingIdentityFailsClosed(t *testing.T) {
	for _, scope := range []TreatmentScope{TreatmentScopeTenant, TreatmentScopeRequest} {
		cfg := RolloutConfig{
			Experiment:     "exp",
			Version:        "v1",
			Enabled:        true,
			ShadowOnly:     true,
			VariantPercent: 50,
			Scope:          scope,
		}
		got := AssignTreatment(cfg, "", "")
		if got.Treatment != TreatmentControl || got.Experiment != "" || got.AssignmentHash != "" {
			t.Errorf("scope %q missing identity = %+v, want fail-closed control", scope, got)
		}
	}
}

func TestAssignTreatmentExperimentAndVersionIsolateAssignments(t *testing.T) {
	base := RolloutConfig{
		Experiment:     "exp-a",
		Version:        "v1",
		Enabled:        true,
		ShadowOnly:     true,
		VariantPercent: 100,
		Scope:          TreatmentScopeRequest,
	}
	a := AssignTreatment(base, "tenant", "request")
	base.Experiment = "exp-b"
	b := AssignTreatment(base, "tenant", "request")
	base.Experiment = "exp-a"
	base.Version = "v2"
	c := AssignTreatment(base, "tenant", "request")
	if a.AssignmentHash == b.AssignmentHash || a.AssignmentHash == c.AssignmentHash {
		t.Fatalf("experiment/version did not isolate assignment hash: a=%+v b=%+v c=%+v", a, b, c)
	}
}

func TestAssignTreatmentInvalidConfigFailsClosed(t *testing.T) {
	cases := []RolloutConfig{
		{Experiment: "", Version: "v1", Enabled: true, VariantPercent: 50, Scope: TreatmentScopeTenant},
		{Experiment: "exp", Version: "", Enabled: true, VariantPercent: 50, Scope: TreatmentScopeTenant},
		{Experiment: "exp", Version: "v1", Enabled: true, VariantPercent: -1, Scope: TreatmentScopeTenant},
		{Experiment: "exp", Version: "v1", Enabled: true, VariantPercent: 101, Scope: TreatmentScopeTenant},
		{Experiment: "exp", Version: "v1", Enabled: true, VariantPercent: 50, Scope: TreatmentScope("bad")},
	}
	for _, cfg := range cases {
		got := AssignTreatment(cfg, "tenant", "request")
		if got.Treatment != TreatmentControl || got.Experiment != "" {
			t.Errorf("invalid config %+v = %+v, want fail-closed control", cfg, got)
		}
	}
}
