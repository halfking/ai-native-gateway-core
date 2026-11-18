package maascontracts

import "time"

// UsageStatus represents the state of a usage ledger entry.
// State transitions per OpenAPI v0.1:
//   held → settled (via UsageSettle)
//   settled → posted (immutable, correction via UsageCorrect append-only)
type UsageStatus string

const (
	UsageStatusHeld    UsageStatus = "held"    // Usage pre-reserved against entitlement
	UsageStatusSettled UsageStatus = "settled" // Usage settled, unused portion released
	UsageStatusPosted  UsageStatus = "posted"  // Finalized ledger entry, immutable
)

// UsageHoldRequest places a hold on usage against an activated entitlement.
// This is a request-level hold, not a second decrement of the inventory hold H.
// The hold reserves anticipated usage before actual consumption is measured.
type UsageHoldRequest struct {
	TenantID      *string  `json:"tenant_id,omitempty"`  // Tenant scope (must match auth)
	EntitlementID string   `json:"entitlement_id"`       // Reference to active entitlement (ent_*)
	RequestID     string   `json:"request_id"`           // Unique request identifier for idempotency
	Quantity      Quantity `json:"quantity"`             // Quantity to reserve
	ModelSKU      *string  `json:"model_sku,omitempty"`  // Optional model SKU reference
}

// UsageHoldResponse returns the created usage hold details.
type UsageHoldResponse struct {
	UsageHoldID string      `json:"usage_hold_id"`        // Unique usage hold identifier (usg_*)
	Status      UsageStatus `json:"status"`               // Should be "held"
	ExpiresAt   *time.Time  `json:"expires_at,omitempty"` // Optional hold expiration
}

// UsageSettleRequest settles a held usage to actual consumption.
// This releases the difference between held and actual quantities back to the entitlement.
// Once settled, the usage becomes part of the billing cycle and cannot be modified
// (only corrected via UsageCorrect).
type UsageSettleRequest struct {
	TenantID          *string  `json:"tenant_id,omitempty"`            // Tenant scope (must match auth)
	Actual            Quantity `json:"actual"`                         // Actual consumed quantity
	StreamInterrupted bool     `json:"stream_interrupted"`             // Whether stream was interrupted (default: false)
}

// UsageSettleResponse returns the settled usage details.
type UsageSettleResponse struct {
	UsageHoldID string      `json:"usage_hold_id"` // Usage hold identifier
	Status      UsageStatus `json:"status"`        // Should be "settled"
	Settled     Quantity    `json:"settled"`       // Settled (actual) quantity
	Released    Quantity    `json:"released"`      // Released (unused) quantity
	EntryID     *string     `json:"entry_id,omitempty"` // Optional ledger entry ID
}

// UsageCorrectRequest appends a correction entry to the ledger.
// This does NOT modify existing posted entries (immutability constraint).
// Instead, it creates a new compensating entry (reversal) for audit trail.
//
// Design rule: Corrections are append-only. Never overwrite posted entries.
type UsageCorrectRequest struct {
	TenantID *string  `json:"tenant_id,omitempty"` // Tenant scope (must match auth)
	EntryID  string   `json:"entry_id"`            // Reference to original ledger entry
	Reversal Quantity `json:"reversal"`            // Reversal quantity (adjustment amount)
	Reason   *string  `json:"reason,omitempty"`    // Optional reason for audit trail
}

// UsageCorrectResponse returns the correction entry details.
type UsageCorrectResponse struct {
	ReversalID string      `json:"reversal_id"`        // New correction entry ID
	EntryID    string      `json:"entry_id"`           // Original entry ID
	Status     *UsageStatus `json:"status,omitempty"`   // Status after correction (should be "posted")
}
