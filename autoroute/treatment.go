package autoroute

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Treatment identifies the experiment arm assigned to a request.
type Treatment string

const (
	TreatmentControl Treatment = "control"
	TreatmentShadow  Treatment = "shadow"
	TreatmentVariant Treatment = "variant"
)

// TreatmentScope controls the identity on which an assignment is stable.
type TreatmentScope string

const (
	TreatmentScopeTenant  TreatmentScope = "tenant"
	TreatmentScopeRequest TreatmentScope = "request"
)

// RolloutConfig describes a V3 rollout. It is safe-by-default: Disabled or
// zero-percent configurations always assign control.
type RolloutConfig struct {
	Experiment     string
	Version        string
	Enabled        bool
	ShadowOnly     bool
	VariantPercent int
	Scope          TreatmentScope
	AutoRollback   bool
}

// Valid reports whether the rollout has a usable experiment identity and
// bounded percentage/scope. Invalid configs fail closed in AssignTreatment.
func (c RolloutConfig) Valid() bool {
	if c.Experiment == "" || c.Version == "" || c.VariantPercent < 0 || c.VariantPercent > 100 {
		return false
	}
	return c.Scope == TreatmentScopeTenant || c.Scope == TreatmentScopeRequest
}

// TreatmentAssignment is the immutable decision-time attribution snapshot.
type TreatmentAssignment struct {
	Experiment     string
	Version        string
	Treatment      Treatment
	Bucket         uint8
	AssignmentHash string
	Scope          TreatmentScope
	ShadowOnly     bool
}

// AssignTreatment deterministically assigns an identity to a rollout arm.
// tenantID is preferred only when ScopeTenant is selected; requestID is used
// for request scope. The raw identity never appears in the result.
func AssignTreatment(cfg RolloutConfig, tenantID, requestID string) TreatmentAssignment {
	assignment := TreatmentAssignment{Treatment: TreatmentControl, Scope: cfg.Scope, ShadowOnly: cfg.ShadowOnly}
	if !cfg.Enabled || !cfg.Valid() || cfg.VariantPercent == 0 {
		return assignment
	}
	key := tenantID
	if cfg.Scope == TreatmentScopeRequest {
		key = requestID
	}
	if key == "" {
		return assignment
	}
	assignment.Experiment = cfg.Experiment
	assignment.Version = cfg.Version
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s", cfg.Experiment, cfg.Version, cfg.Scope, key)))
	assignment.AssignmentHash = hex.EncodeToString(digest[:8])
	assignment.Bucket = digest[0] % 100
	if int(assignment.Bucket) < cfg.VariantPercent {
		if cfg.ShadowOnly {
			assignment.Treatment = TreatmentShadow
		} else {
			assignment.Treatment = TreatmentVariant
		}
	}
	return assignment
}
