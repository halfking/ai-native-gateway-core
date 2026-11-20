package maascontracts

import "time"

// EntitlementStatus represents the state of a customer entitlement.
// Per contract v0.1, entitlements can only be activated after a committed hold.
type EntitlementStatus string

const (
	EntitlementStatusActive EntitlementStatus = "active" // Customer can consume the resource
)

// ActivateEntitlementRequest activates customer entitlement after committed reservation.
// Preconditions per OpenAPI v0.1:
//   - Referenced hold must be in "committed" status
//   - Legal signoff reference should be present (contract enforcement depends on business rules)
type ActivateEntitlementRequest struct {
	TenantID        *string `json:"tenant_id,omitempty"`         // Tenant scope (must match auth)
	HoldID          string  `json:"hold_id"`                     // Reference to committed hold (hld_*)
	LegalSignoffRef *string `json:"legal_signoff_ref,omitempty"` // External legal/payment reference
	ContractID      *string `json:"contract_id,omitempty"`       // Optional contract reference (ctr_*)
}

// ActivateEntitlementResponse returns the activated entitlement details.
type ActivateEntitlementResponse struct {
	EntitlementID string            `json:"entitlement_id"`         // Unique entitlement identifier (ent_*)
	Status        EntitlementStatus `json:"status"`                 // Should be "active"
	HoldID        string            `json:"hold_id"`                // Referenced hold ID
	ActivatedAt   *time.Time        `json:"activated_at,omitempty"` // Activation timestamp
}
