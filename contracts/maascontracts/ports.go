package maascontracts

import "context"

// InventoryPort defines the interface for inventory and reservation operations.
// This port handles sellable queries and hold state machine transitions per OpenAPI v0.1.
type InventoryPort interface {
	// QuerySellable returns the current snapshot of available sellable items.
	// This is a read-only operation with no side effects.
	// Scopes required: maas.inventory.read
	QuerySellable(ctx context.Context, auth AuthContext, req QuerySellableRequest) (QuerySellableResponse, error)

	// HoldCreate initiates a new pending reservation hold (soft reserve).
	// The hold reserves inventory for a limited time period until expiration.
	// Scopes required: maas.inventory.reserve
	HoldCreate(ctx context.Context, auth AuthContext, meta CommandMeta, req HoldCreateRequest) (HoldCreateResponse, error)

	// HoldConfirm commits a pending hold to a hard reservation (committed state).
	// This state transition is irreversible and decrements available inventory.
	// Scopes required: maas.inventory.commit
	HoldConfirm(ctx context.Context, auth AuthContext, meta CommandMeta, holdID string, req HoldConfirmRequest) (HoldConfirmResponse, error)

	// HoldRelease releases a hold (pending or committed) back to inventory.
	// Committed holds should only be released in exceptional circumstances (e.g., cancellation).
	// Scopes required: maas.inventory.reserve
	HoldRelease(ctx context.Context, auth AuthContext, meta CommandMeta, holdID string, req HoldReleaseRequest) (HoldReleaseResponse, error)

	// HoldExpire marks an expired hold (system-initiated or client-triggered).
	// The inventory service has authoritative decision on expiration timing.
	// Scopes required: maas.inventory.reserve
	HoldExpire(ctx context.Context, auth AuthContext, meta CommandMeta, holdID string, req HoldExpireRequest) (HoldExpireResponse, error)
}

// QuotePort defines the interface for quoting operations per OpenAPI v0.1.
// Quoting is a pure function operation with no side effects or state changes.
type QuotePort interface {
	// EstimateQuote produces a non-binding price estimate.
	// This operation does NOT create holds or modify any state.
	// Per contract v0.1: EstimateQuote MUST NOT create an inventory hold.
	// Scopes required: maas.quote.estimate
	EstimateQuote(ctx context.Context, auth AuthContext, req EstimateQuoteRequest) (EstimateQuoteResponse, error)
}

// EntitlementPort defines the interface for customer entitlement operations per OpenAPI v0.1.
// Entitlements can only be activated after a committed hold is in place.
type EntitlementPort interface {
	// ActivateEntitlement activates customer entitlement after committed reservation.
	// Preconditions:
	//   - Referenced hold must be in "committed" status
	//   - Legal signoff reference should be present (business rules apply)
	// Scopes required: maas.entitlement.activate
	ActivateEntitlement(ctx context.Context, auth AuthContext, meta CommandMeta, req ActivateEntitlementRequest) (ActivateEntitlementResponse, error)
}

// LedgerPort defines the interface for usage tracking and settlement operations per OpenAPI v0.1.
// The ledger maintains an append-only immutable audit trail of usage and corrections.
type LedgerPort interface {
	// UsageHold places a hold on usage against an activated entitlement.
	// This is a request-level hold, not a second decrement of inventory hold H.
	// Scopes required: maas.ledger.write
	UsageHold(ctx context.Context, auth AuthContext, meta CommandMeta, req UsageHoldRequest) (UsageHoldResponse, error)

	// UsageSettle settles actual usage and releases unused remainder.
	// This state transition is irreversible. Once settled, the usage becomes
	// part of the billing cycle and cannot be modified (only corrected via UsageCorrect).
	// Scopes required: maas.ledger.write
	UsageSettle(ctx context.Context, auth AuthContext, meta CommandMeta, usageHoldID string, req UsageSettleRequest) (UsageSettleResponse, error)

	// UsageCorrect appends a correction entry to the ledger (append-only, never modifies posted entries).
	// This creates a reversal entry for audit trail without modifying the original entry.
	// Scopes required: maas.ledger.write
	UsageCorrect(ctx context.Context, auth AuthContext, meta CommandMeta, req UsageCorrectRequest) (UsageCorrectResponse, error)
}

// UnitOfWork defines the interface for transactional boundaries.
// Implementations coordinate multi-port operations within a single transaction.
// This is particularly important for operations that span inventory, entitlement, and ledger domains.
type UnitOfWork interface {
	// Execute runs the given function within a transactional boundary.
	// All port operations within the function participate in the same transaction.
	// If the function returns an error, the transaction is rolled back.
	Execute(ctx context.Context, fn func(ctx context.Context) error) error
}
