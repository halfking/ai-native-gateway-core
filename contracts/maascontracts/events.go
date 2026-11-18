package maascontracts

import "time"

// CloudEventEnvelope wraps event data in CloudEvents 1.0 format.
// All MaaS events follow this standard envelope for async communication.
// Reference: https://github.com/cloudevents/spec/blob/v1.0/spec.md
type CloudEventEnvelope struct {
	SpecVersion     string                 `json:"specversion"`          // CloudEvents version (should be "1.0")
	Type            string                 `json:"type"`                 // Event type (e.g., "com.maas.reservation.committed")
	Source          string                 `json:"source"`               // Event source URI
	ID              string                 `json:"id"`                   // Unique event ID (evt_*)
	Time            time.Time              `json:"time"`                 // Event timestamp (RFC3339)
	DataContentType string                 `json:"datacontenttype"`      // MIME type (should be "application/json")
	Data            interface{}            `json:"data"`                 // Event-specific data payload
	Extensions      map[string]interface{} `json:"extensions,omitempty"` // Optional extensions (e.g., tenant_id, trace_id)
}

// Event type constants for CloudEvents.
// These align with AsyncAPI contract (referenced in OpenAPI v0.1 documentation).
const (
	EventTypeSellableChanged      = "com.maas.sellable.changed"
	EventTypeReservationPending   = "com.maas.reservation.pending"
	EventTypeReservationCommitted = "com.maas.reservation.committed"
	EventTypeReservationExpired   = "com.maas.reservation.expired"
	EventTypeReservationAborted   = "com.maas.reservation.aborted"
	EventTypeEntitlementActivated = "com.maas.entitlement.activated"
	EventTypeUsageHoldPlaced      = "com.maas.usage.hold_placed"
	EventTypeUsageSettled         = "com.maas.usage.settled"
	EventTypeUsageCorrected       = "com.maas.usage.corrected"
)

// SellableChangedData represents inventory availability change event.
type SellableChangedData struct {
	ModelSKU          string    `json:"model_sku"`
	TenantID          string    `json:"tenant_id"`
	AvailableQuantity Quantity  `json:"available_quantity"`
	ChangedAt         time.Time `json:"changed_at"`
}

// ReservationPendingData represents a new pending hold created event.
type ReservationPendingData struct {
	HoldID    string     `json:"hold_id"`
	TenantID  string     `json:"tenant_id"`
	Status    HoldStatus `json:"status"` // Should be "pending"
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// ReservationCommittedData represents a hold confirmed to committed state event.
type ReservationCommittedData struct {
	HoldID      string     `json:"hold_id"`
	TenantID    string     `json:"tenant_id"`
	Status      HoldStatus `json:"status"` // Should be "committed"
	ConfirmedAt time.Time  `json:"confirmed_at"`
}

// ReservationExpiredData represents a hold expired due to TTL event.
type ReservationExpiredData struct {
	HoldID    string     `json:"hold_id"`
	TenantID  string     `json:"tenant_id"`
	Status    HoldStatus `json:"status"` // Should be "expired"
	ExpiredAt time.Time  `json:"expired_at"`
}

// ReservationAbortedData represents a hold explicitly released event.
type ReservationAbortedData struct {
	HoldID     string     `json:"hold_id"`
	TenantID   string     `json:"tenant_id"`
	Status     HoldStatus `json:"status"` // Should be "aborted"
	Reason     *string    `json:"reason,omitempty"`
	ReleasedAt time.Time  `json:"released_at"`
}

// EntitlementActivatedData represents customer entitlement activation event.
type EntitlementActivatedData struct {
	EntitlementID string            `json:"entitlement_id"`
	TenantID      string            `json:"tenant_id"`
	HoldID        string            `json:"hold_id"`
	Status        EntitlementStatus `json:"status"` // Should be "active"
	ActivatedAt   time.Time         `json:"activated_at"`
}

// UsageHoldPlacedData represents usage pre-reservation event.
type UsageHoldPlacedData struct {
	UsageHoldID   string      `json:"usage_hold_id"`
	TenantID      string      `json:"tenant_id"`
	EntitlementID string      `json:"entitlement_id"`
	Quantity      Quantity    `json:"quantity"`
	Status        UsageStatus `json:"status"` // Should be "held"
	HeldAt        time.Time   `json:"held_at"`
}

// UsageSettledData represents usage settlement to payable charge event.
type UsageSettledData struct {
	UsageHoldID   string      `json:"usage_hold_id"`
	TenantID      string      `json:"tenant_id"`
	EntitlementID string      `json:"entitlement_id"`
	Settled       Quantity    `json:"settled"`
	Released      Quantity    `json:"released"`
	Status        UsageStatus `json:"status"` // Should be "settled"
	SettledAt     time.Time   `json:"settled_at"`
}

// UsageCorrectedData represents an append-only correction entry event.
type UsageCorrectedData struct {
	ReversalID  string    `json:"reversal_id"`
	TenantID    string    `json:"tenant_id"`
	EntryID     string    `json:"entry_id"`
	Reversal    Quantity  `json:"reversal"`
	Reason      *string   `json:"reason,omitempty"`
	CorrectedAt time.Time `json:"corrected_at"`
}
