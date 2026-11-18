package maascontracts

import "time"

// Money represents monetary values using decimal string for precision.
// AmountMinor is the amount in the smallest currency unit (e.g., cents for USD).
// Example: {AmountMinor: "1250", Currency: "USD"} represents $12.50.
//
// Contract v0.1 explicitly forbids IEEE binary floating point (float64) as
// the money exchange format to avoid precision loss in financial calculations.
type Money struct {
	AmountMinor string `json:"amount_minor"` // Integer minor units as decimal string (e.g., "1250" for $12.50)
	Currency    string `json:"currency"`     // ISO 4217 3-character code (e.g., "USD", "CNY")
}

// Quantity represents a measured amount with its unit and optional time window.
// Amount is represented as a decimal string to avoid floating point precision issues.
type Quantity struct {
	Amount string  `json:"amount"`           // Decimal string representation (e.g., "1000", "1000.5")
	Unit   string  `json:"unit"`             // Unit of measurement (e.g., "token", "request", "rpm")
	Window *string `json:"window,omitempty"` // Optional time window (e.g., "monthly", "daily")
}

// AuthContext carries authentication and authorization context for commands and queries.
// It is extracted from the trusted authentication layer and passed to port methods.
type AuthContext struct {
	TenantID string   `json:"tenant_id"` // Authoritative tenant ID from auth layer
	Subject  string   `json:"subject"`   // Authenticated subject (user/service ID)
	Scopes   []string `json:"scopes"`    // Granted permission scopes (e.g., "maas.inventory.read")
	TraceID  string   `json:"trace_id"`  // Distributed tracing ID
	SpanID   string   `json:"span_id"`   // Current span ID
}

// CommandMeta carries idempotency and optimistic concurrency control metadata.
// All write commands (creates, updates, state transitions) must include idempotency key via HTTP header.
type CommandMeta struct {
	IdempotencyKey  string `json:"idempotency_key"`            // Required for all commands (from Idempotency-Key header)
	ExpectedVersion *int64 `json:"expected_version,omitempty"` // Optional OCC version check
}

// TimeRange represents a time period with start and end timestamps.
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
