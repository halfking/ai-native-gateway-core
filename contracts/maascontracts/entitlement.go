package maascontracts

import "time"

// EntitlementStatus represents the state of a customer entitlement.
// Entitlements can only be activated after a committed hold is in place.
type EntitlementStatus string

const (
	EntitlementStatusActive    EntitlementStatus = "active"    // Customer can consume the resource
	EntitlementStatusSuspended EntitlementStatus = "suspended" // Temporarily paused
	EntitlementStatusRevoked   EntitlementStatus = "revoked"   // Permanently terminated
)

// ActivateEntitlementRequest activates customer entitlement after committed reservation.
// This operation requires a committed hold and legal agreement confirmation.
// Preconditions:
//   - Referenced hold must be in "committed" status
//   - LegalSignoffRef must be present and valid
type ActivateEntitlementRequest struct {
	TenantID        string            `json:"tenant_id"`
	HoldID          string            `json:"hold_id"`                // Reference to committed hold
	CustomerID      string            `json:"customer_id"`            // End customer identifier
	LegalSignoffRef string            `json:"legal_signoff_ref"`      // Required legal agreement reference
	EffectiveAt     *time.Time        `json:"effective_at,omitempty"` // Optional start time (default: now)
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`   // Optional expiry time
	Metadata        map[string]string `json:"metadata,omitempty"`     // Optional custom metadata
}

// ActivateEntitlementResponse returns the activated entitlement details.
type ActivateEntitlementResponse struct {
	EntitlementID   string            `json:"entitlement_id"` // Unique entitlement identifier (ent_*)
	Status          EntitlementStatus `json:"status"`         // Should be "active"
	HoldID          string            `json:"hold_id"`
	CustomerID      string            `json:"customer_id"`
	LegalSignoffRef string            `json:"legal_signoff_ref"`
	EffectiveAt     time.Time         `json:"effective_at"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
	ActivatedAt     time.Time         `json:"activated_at"`
}
