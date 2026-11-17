package maascontracts

import "time"

// HoldStatus represents the state of a reservation hold in the state machine.
// State transitions:
//
//	pending → committed (via HoldConfirm)
//	pending → aborted (via HoldRelease or validation failure)
//	pending → expired (via TTL expiration or HoldExpire)
//	committed → aborted (via HoldRelease after commit)
type HoldStatus string

const (
	HoldStatusPending   HoldStatus = "pending"   // Initial state, awaiting confirmation
	HoldStatusCommitted HoldStatus = "committed" // Confirmed hard reserve, ready for entitlement activation
	HoldStatusExpired   HoldStatus = "expired"   // TTL exceeded, inventory released
	HoldStatusAborted   HoldStatus = "aborted"   // Explicitly released or validation failed
)

// QuerySellableRequest queries the current snapshot of sellable inventory.
// This is a read-only operation with no side effects.
type QuerySellableRequest struct {
	TenantID  string    `json:"tenant_id"`           // Tenant scope filter
	Labels    *[]string `json:"labels,omitempty"`    // Optional label filters
	Timestamp *string   `json:"timestamp,omitempty"` // Optional ISO8601 timestamp for point-in-time query
}

// QuerySellableResponse returns the current inventory snapshot.
type QuerySellableResponse struct {
	Items     []SellableItem `json:"items"`
	Timestamp time.Time      `json:"timestamp"` // Snapshot timestamp
}

// SellableItem represents a sellable resource in the inventory.
type SellableItem struct {
	SellableID        string   `json:"sellable_id"` // Unique sellable identifier (sel_*)
	CanonicalName     string   `json:"canonical_name"`
	DisplayName       string   `json:"display_name"`
	Unit              string   `json:"unit"` // Unit of measurement
	AvailableQuantity Quantity `json:"available_quantity"`
	PricePerUnit      Money    `json:"price_per_unit"`
	Labels            []string `json:"labels,omitempty"`
}

// HoldCreateRequest initiates a new reservation hold (pending state).
// The hold reserves inventory for a limited TTL period.
type HoldCreateRequest struct {
	TenantID    string     `json:"tenant_id"`
	Lines       []HoldLine `json:"lines"`                  // Line items to reserve
	TTLSeconds  int        `json:"ttl_seconds"`            // Time-to-live in seconds
	ReferenceID *string    `json:"reference_id,omitempty"` // Optional external reference
	PurposeNote *string    `json:"purpose_note,omitempty"` // Optional purpose description
}

// HoldCreateResponse returns the created hold details.
type HoldCreateResponse struct {
	HoldID       string     `json:"hold_id"` // Unique hold identifier (hld_*)
	Status       HoldStatus `json:"status"`  // Should be "pending"
	Lines        []HoldLine `json:"lines"`
	ExpiresAt    time.Time  `json:"expires_at"`    // Expiration timestamp (created_at + TTL)
	FencingToken string     `json:"fencing_token"` // Token for idempotent confirm/release
	CreatedAt    time.Time  `json:"created_at"`
}

// HoldLine represents a single line item in a reservation hold.
type HoldLine struct {
	SellableID     string   `json:"sellable_id"`
	Quantity       Quantity `json:"quantity"`
	UnitPrice      Money    `json:"unit_price"` // Locked price at hold time
	SubtotalAmount Money    `json:"subtotal_amount"`
}

// HoldConfirmRequest commits a pending hold to a hard reservation.
// This state transition is irreversible and makes the inventory unavailable
// for other customers until consumed or explicitly released.
type HoldConfirmRequest struct {
	HoldID       string `json:"hold_id"`
	FencingToken string `json:"fencing_token"` // Must match the token from HoldCreate
}

// HoldConfirmResponse returns the confirmed hold details.
type HoldConfirmResponse struct {
	HoldID      string     `json:"hold_id"`
	Status      HoldStatus `json:"status"` // Should be "committed"
	ConfirmedAt time.Time  `json:"confirmed_at"`
}

// HoldReleaseRequest releases a hold (pending or committed) back to inventory.
// Committed holds should only be released in exceptional circumstances (e.g., customer cancellation).
type HoldReleaseRequest struct {
	HoldID       string  `json:"hold_id"`
	FencingToken string  `json:"fencing_token"`
	Reason       *string `json:"reason,omitempty"` // Optional reason for audit trail
}

// HoldReleaseResponse confirms the hold release.
type HoldReleaseResponse struct {
	HoldID     string     `json:"hold_id"`
	Status     HoldStatus `json:"status"` // Should be "aborted"
	ReleasedAt time.Time  `json:"released_at"`
}

// HoldExpireRequest marks an expired pending hold (system-initiated).
// This is typically called by a background job when TTL expires.
type HoldExpireRequest struct {
	HoldID    string    `json:"hold_id"`
	ExpiredAt time.Time `json:"expired_at"`
}

// HoldExpireResponse confirms the hold expiration.
type HoldExpireResponse struct {
	HoldID    string     `json:"hold_id"`
	Status    HoldStatus `json:"status"` // Should be "expired"
	ExpiredAt time.Time  `json:"expired_at"`
}
