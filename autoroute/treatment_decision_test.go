package autoroute

import "testing"

func TestAnnotateTreatmentFailsClosedWithoutAssignmentIdentity(t *testing.T) {
	d := &Decider{treatmentRollout: &RolloutConfig{
		Experiment:     "auto-v3",
		Version:        "v1",
		Enabled:        true,
		VariantPercent: 100,
		Scope:          TreatmentScopeTenant,
	}}
	decision := &Decision{}
	d.annotateTreatment(nil, 7, decision)
	if decision.ExperimentID != "" || decision.AssignmentKeyHash != "" {
		t.Fatalf("identity-less assignment must remain unenrolled: %+v", decision)
	}
}

func TestAnnotateTreatmentUsesTenantResolver(t *testing.T) {
	d := &Decider{
		treatmentRollout: &RolloutConfig{
			Experiment:     "auto-v3",
			Version:        "v1",
			Enabled:        true,
			VariantPercent: 100,
			Scope:          TreatmentScopeTenant,
		},
		TenantResolver: func(apiKeyID int) string {
			if apiKeyID != 7 {
				t.Fatalf("api key id = %d, want 7", apiKeyID)
			}
			return "tenant-a"
		},
	}
	decision := &Decision{}
	d.annotateTreatment(nil, 7, decision)
	if decision.ExperimentID != "auto-v3" || decision.AssignmentVersion != "v1" || decision.Treatment != TreatmentVariant || decision.AssignmentKeyHash == "" {
		t.Fatalf("assignment = %+v, want enrolled variant", decision)
	}
}
