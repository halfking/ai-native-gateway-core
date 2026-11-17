package maascontracts

import "time"

// UsageStatus represents the state of a usage ledger entry.
// State transitions:
//
//	held → settled (via UsageSettle)
//	settled → posted (immutable, correction via UsageCorrect append-only)
type UsageStatus string

const (
	UsageStatusHeld    UsageStatus = "held"    // Usage pre-reserved, can be released or settled
	UsageStatusSettled UsageStatus = "settled" // Usage settled to payable, awaiting posting
	UsageStatusPosted  UsageStatus = "posted"  // Finalized ledger entry, immutable
)

// UsageHoldRequest places a hold on usage (pre-reservation before actual consumption).
// This allows the system to track anticipated charges before finalizing them.
type UsageHoldRequest struct {
	TenantID      string            `json:"tenant_id"`
	EntitlementID string            `json:"entitlement_id"` // Reference to active entitlement
	UsageType     string            `json:"usage_type"`     // Type of usage (e.g., "api_calls", "tokens")
	Quantity      Quantity          `json:"quantity"`
	EstimatedCost Money             `json:"estimated_cost"`
	MeasuredAt    time.Time         `json:"measured_at"`        // When usage was measured
	Metadata      map[string]string `json:"metadata,omitempty"` // Optional custom metadata
}

// UsageHoldResponse returns the created usage hold details.
type UsageHoldResponse struct {
	UsageID       string      `json:"usage_id"` // Unique usage identifier (usg_*)
	Status        UsageStatus `json:"status"`   // Should be "held"
	EntitlementID string      `json:"entitlement_id"`
	UsageType     string      `json:"usage_type"`
	Quantity      Quantity    `json:"quantity"`
	EstimatedCost Money       `json:"estimated_cost"`
	MeasuredAt    time.Time   `json:"measured_at"`
	HeldAt        time.Time   `json:"held_at"`
}

// UsageSettleRequest settles a held usage to a payable charge.
// This state transition is irreversible. Once settled, the usage becomes
// part of the billing cycle and cannot be modified (only corrected via UsageCorrect).
//
// Design rule: settled usage + release conflict → settled wins (409 on release attempt).
type UsageSettleRequest struct {
	UsageID     string  `json:"usage_id"`               // Reference to held usage
	FinalCost   Money   `json:"final_cost"`             // Actual cost after metering/adjustment
	SettledNote *string `json:"settled_note,omitempty"` // Optional settlement note
}

// UsageSettleResponse returns the settled usage details.
type UsageSettleResponse struct {
	UsageID   string      `json:"usage_id"`
	Status    UsageStatus `json:"status"` // Should be "settled"
	FinalCost Money       `json:"final_cost"`
	SettledAt time.Time   `json:"settled_at"`
}

// UsageCorrectRequest appends a correction entry to the ledger.
// This does NOT modify existing posted entries (immutability constraint).
// Instead, it creates a new compensating entry for audit trail.
//
// Design rule: Corrections are append-only. Never overwrite posted entries.
type UsageCorrectRequest struct {
	TenantID         string `json:"tenant_id"`
	OriginalUsageID  string `json:"original_usage_id"` // Reference to original usage entry
	CorrectionType   string `json:"correction_type"`   // Type of correction (e.g., "refund", "adjustment")
	CorrectionAmount Money  `json:"correction_amount"` // Correction amount (can be negative)
	Reason           string `json:"reason"`            // Required reason for audit trail
}

// UsageCorrectResponse returns the correction entry details.
type UsageCorrectResponse struct {
	CorrectionID     string    `json:"correction_id"` // New correction entry ID (usg_*)
	OriginalUsageID  string    `json:"original_usage_id"`
	CorrectionType   string    `json:"correction_type"`
	CorrectionAmount Money     `json:"correction_amount"`
	Reason           string    `json:"reason"`
	CorrectedAt      time.Time `json:"corrected_at"`
}
