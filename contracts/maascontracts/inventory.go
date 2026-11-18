package maascontracts

import "time"

// HoldStatus represents the state of a reservation hold in the state machine.
// State transitions per OpenAPI v0.1:
//   pending → committed (via HoldConfirm)
//   pending → aborted (via HoldRelease)
//   pending → expired (via HoldExpire or TTL timeout)
type HoldStatus string

const (
	HoldStatusPending   HoldStatus = "pending"   // Initial state, soft reserve awaiting confirmation
	HoldStatusCommitted HoldStatus = "committed" // Confirmed hard reserve, inventory decremented
	HoldStatusExpired   HoldStatus = "expired"   // TTL exceeded, inventory released
	HoldStatusAborted   HoldStatus = "aborted"   // Explicitly released
)

// QuerySellableRequest queries the current snapshot of sellable inventory.
// This is a read-only operation with no side effects.
type QuerySellableRequest struct {
	TenantID   *string                `json:"tenant_id,omitempty"`  // If present must equal authenticated tenant
	ModelSKU   string                 `json:"model_sku"`            // Model SKU to query
	Dimensions *QuerySellableDimensions `json:"dimensions,omitempty"` // Optional dimension filters
}

// QuerySellableDimensions provides optional filters for sellable queries.
type QuerySellableDimensions struct {
	Region     *string `json:"region,omitempty"`     // Regional filter
	Commitment *string `json:"commitment,omitempty"` // "hard" or "soft"
}

// QuerySellableResponse returns the current inventory snapshot.
type QuerySellableResponse struct {
	ResourceSnapshotID string         `json:"resource_snapshot_id"` // Snapshot identifier
	InventoryRevision  int64          `json:"inventory_revision"`   // Optimistic concurrency version
	ObservedAt         time.Time      `json:"observed_at"`          // Snapshot timestamp
	Stale              bool           `json:"stale"`                // Whether snapshot is stale (default: false)
	Items              []SellableItem `json:"items"`                // Available sellable items
}

// SellableItem represents a sellable resource in the inventory snapshot.
type SellableItem struct {
	ModelSKU          string   `json:"model_sku"`                    // Model SKU identifier
	Sellable          Quantity `json:"sellable"`                     // Available quantity with unit and window
	Unit              string   `json:"unit"`                         // Unit of measurement
	CapacityAvailable *bool    `json:"capacity_available,omitempty"` // Capacity calendar indicator
	SharedCapID       *string  `json:"shared_cap_id,omitempty"`      // Shared supplier cap ID
}

// HoldLine represents a single line item in a reservation hold.
type HoldLine struct {
	ModelSKU    string   `json:"model_sku"`              // Model SKU to reserve
	Quantity    Quantity `json:"quantity"`               // Quantity to reserve
	SharedCapID *string  `json:"shared_cap_id,omitempty"` // Optional shared capacity ID
}

// HoldCreateRequest initiates a new reservation hold (pending state).
// The hold reserves inventory for a limited time period until expiration.
type HoldCreateRequest struct {
	TenantID           *string    `json:"tenant_id,omitempty"`            // Tenant scope (must match auth)
	QuoteSnapshotID    *string    `json:"quote_snapshot_id,omitempty"`    // Optional reference to quote
	ResourceSnapshotID *string    `json:"resource_snapshot_id,omitempty"` // Optional snapshot ID for validation
	ExpectedVersion    *int64     `json:"expected_version,omitempty"`     // Optional OCC version check
	Lines              []HoldLine `json:"lines"`                          // Line items to reserve (min 1)
	ExpiresAt          time.Time  `json:"expires_at"`                     // Hold expiration timestamp
}

// HoldCreateResponse returns the created hold details.
type HoldCreateResponse struct {
	HoldID            string     `json:"hold_id"`            // Unique hold identifier (hld_*)
	Status            HoldStatus `json:"status"`             // Should be "pending"
	FencingToken      string     `json:"fencing_token"`      // Token for idempotent confirm/release
	InventoryRevision int64      `json:"inventory_revision"` // New inventory revision after hold
	ExpiresAt         time.Time  `json:"expires_at"`         // Hold expiration timestamp
}

// HoldConfirmRequest commits a pending hold to a hard reservation (committed state).
// This state transition is irreversible and makes inventory unavailable to others.
type HoldConfirmRequest struct {
	TenantID        *string `json:"tenant_id,omitempty"`          // Tenant scope (must match auth)
	FencingToken    string  `json:"fencing_token"`                // Must match token from HoldCreate
	ExpectedVersion *int64  `json:"expected_version,omitempty"`   // Optional OCC version check
	QuoteSnapshotID *string `json:"quote_snapshot_id,omitempty"`  // Optional quote snapshot reference
}

// HoldConfirmResponse confirms the hold commitment.
type HoldConfirmResponse struct {
	HoldID            string     `json:"hold_id"`            // Hold identifier
	Status            HoldStatus `json:"status"`             // Should be "committed"
	InventoryRevision int64      `json:"inventory_revision"` // New inventory revision
	CommittedAt       *time.Time `json:"committed_at,omitempty"` // Commitment timestamp
}

// HoldReleaseRequest releases a hold (pending or committed) back to inventory.
// Committed holds should only be released in exceptional circumstances (e.g., cancellation).
type HoldReleaseRequest struct {
	TenantID     *string `json:"tenant_id,omitempty"`     // Tenant scope (must match auth)
	FencingToken *string `json:"fencing_token,omitempty"` // Optional fencing token
	Reason       *string `json:"reason,omitempty"`        // Optional reason for audit trail
}

// HoldReleaseResponse confirms the hold release.
type HoldReleaseResponse struct {
	HoldID string     `json:"hold_id"` // Hold identifier
	Status HoldStatus `json:"status"`  // Terminal status: "aborted", "expired", or "committed"
}

// HoldExpireRequest marks an expired hold (system-initiated or client-triggered).
// The inventory service has authoritative decision on expiration.
type HoldExpireRequest struct {
	TenantID *string `json:"tenant_id,omitempty"` // Tenant scope (must match auth)
}

// HoldExpireResponse confirms the hold expiration.
type HoldExpireResponse struct {
	HoldID  string     `json:"hold_id"`            // Hold identifier
	Status  HoldStatus `json:"status"`             // Terminal status after expiration
	Expired *bool      `json:"expired,omitempty"`  // Whether actually expired
}
