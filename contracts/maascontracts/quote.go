package maascontracts

import "time"

// QuoteStatus represents the lifecycle state of a quote.
// Quoting is a read-only operation that produces a non-binding price estimate.
type QuoteStatus string

const (
	QuoteStatusEstimate QuoteStatus = "estimate" // Non-binding price estimate
	QuoteStatusBinding  QuoteStatus = "binding"  // Binding quote (reserved inventory)
)

// EstimateQuoteRequest requests a non-binding price quote.
// This is a pure function operation with no side effects or state changes.
type EstimateQuoteRequest struct {
	TenantID   string            `json:"tenant_id"`
	Lines      []QuoteLineInput  `json:"lines"`                 // Line items to quote
	ValidUntil *time.Time        `json:"valid_until,omitempty"` // Optional expiry time
	Metadata   map[string]string `json:"metadata,omitempty"`    // Optional custom metadata
}

// QuoteLineInput specifies a line item for quoting.
type QuoteLineInput struct {
	SellableID string   `json:"sellable_id"`
	Quantity   Quantity `json:"quantity"`
}

// EstimateQuoteResponse returns the non-binding price quote.
type EstimateQuoteResponse struct {
	QuoteID       string          `json:"quote_id"` // Unique quote identifier (quo_*)
	Status        QuoteStatus     `json:"status"`   // Should be "estimate"
	Lines         []QuoteLineItem `json:"lines"`
	TotalAmount   Money           `json:"total_amount"`
	Currency      string          `json:"currency"`
	QuotedAt      time.Time       `json:"quoted_at"`
	ValidUntil    *time.Time      `json:"valid_until,omitempty"`
	HoldClaimNote *string         `json:"hold_claim_note,omitempty"` // Note: quote never claims hold (design rule)
}

// QuoteLineItem represents a quoted line item with pricing breakdown.
type QuoteLineItem struct {
	SellableID     string   `json:"sellable_id"`
	CanonicalName  string   `json:"canonical_name"`
	DisplayName    string   `json:"display_name"`
	Quantity       Quantity `json:"quantity"`
	UnitPrice      Money    `json:"unit_price"`
	SubtotalAmount Money    `json:"subtotal_amount"`
	DiscountAmount *Money   `json:"discount_amount,omitempty"`
}
