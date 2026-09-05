package autoroute

import "context"

// annotateTreatment attaches an immutable, privacy-preserving rollout snapshot
// to a decision. Invalid, disabled, or identity-less configurations remain
// unenrolled so a control decision cannot be mistaken for experiment data.
func (d *Decider) annotateTreatment(ctx context.Context, apiKeyID int, decision *Decision) {
	if d == nil || decision == nil {
		return
	}

	var cfg RolloutConfig
	if d.treatmentRollout != nil {
		cfg = *d.treatmentRollout
	} else if flags := GetFeatureFlags(); flags != nil {
		cfg = RolloutConfig{
			Experiment:     flags.AutoOptimizationV3Experiment,
			Version:        flags.AutoOptimizationV3Version,
			Enabled:        flags.AutoOptimizationV3Enabled,
			ShadowOnly:     flags.AutoOptimizationV3ShadowOnly,
			VariantPercent: flags.AutoOptimizationV3VariantPct,
			Scope:          flags.AutoOptimizationV3Scope,
			AutoRollback:   flags.AutoOptimizationV3AutoRollback,
		}
	}

	if !cfg.Enabled || !cfg.Valid() {
		return
	}

	tenantID := ""
	if d.TenantResolver != nil {
		tenantID = d.TenantResolver(apiKeyID)
	}
	assignment := AssignTreatment(cfg, tenantID, requestIDFromContext(ctx))
	if assignment.Experiment == "" {
		return
	}
	decision.ExperimentID = assignment.Experiment
	decision.Treatment = assignment.Treatment
	decision.AssignmentVersion = assignment.Version
	decision.AssignmentKeyHash = assignment.AssignmentHash
}
