package maascontracts

import "time"

// QuoteStatus represents the lifecycle state of a quote.
// Per OpenAPI v0.1, EstimateQuote produces reference (non-binding) quotes only.
type QuoteStatus string

const (
	QuoteStatusReference QuoteStatus = "reference" // Non-binding price estimate (no inventory hold)
)

// EstimateQuoteRequest requests a non-binding price quote.
// This is a pure function operation with no side effects or state changes.
// Per contract v0.1: EstimateQuote MUST NOT create an inventory hold.
type EstimateQuoteRequest struct {
	TenantID           *string  `json:"tenant_id,omitempty"`            // Tenant scope (must match auth)
	ModelSKU           string   `json:"model_sku"`                      // Model SKU to quote
	Quantity           Quantity `json:"quantity"`                       // Quantity to quote
	ResourceSnapshotID string   `json:"resource_snapshot_id"`           // Required snapshot ID for consistency
	PriceVersionID     *string  `json:"price_version_id,omitempty"`     // Optional price version reference
}

// EstimateQuoteResponse returns the non-binding price quote.
// Per contract v0.1: hold_created must be false for EstimateQuote.
type EstimateQuoteResponse struct {
	QuoteID            string      `json:"quote_id"`             // Unique quote identifier (quo_*)
	QuoteSnapshotID    string      `json:"quote_snapshot_id"`    // Quote snapshot identifier
	PriceVersionID     string      `json:"price_version_id"`     // Price version used
	ResourceSnapshotID string      `json:"resource_snapshot_id"` // Resource snapshot reference
	Status             QuoteStatus `json:"status"`               // Should be "reference"
	Money              Money       `json:"money"`                // Total cost estimate
	ExpiresAt          *time.Time  `json:"expires_at,omitempty"` // Optional quote expiration
	HoldCreated        bool        `json:"hold_created"`         // Must be false for EstimateQuote
}
