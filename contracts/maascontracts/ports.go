package maascontracts

import "context"

// InventoryPort defines the interface for inventory and reservation operations.
// This port handles sellable queries and hold state machine transitions.
type InventoryPort interface {
	// QuerySellable returns the current snapshot of available sellable items.
	// This is a read-only operation with no side effects.
	QuerySellable(ctx context.Context, auth AuthContext, req QuerySellableRequest) (QuerySellableResponse, error)

	// HoldCreate initiates a new pending reservation hold.
	// The hold reserves inventory for a limited TTL period.
	HoldCreate(ctx context.Context, auth AuthContext, meta CommandMeta, req HoldCreateRequest) (HoldCreateResponse, error)

	// HoldConfirm commits a pending hold to a hard reservation (committed state).
	// This state transition is irreversible.
	HoldConfirm(ctx context.Context, auth AuthContext, meta CommandMeta, req HoldConfirmRequest) (HoldConfirmResponse, error)

	// HoldRelease releases a hold (pending or committed) back to inventory.
	// Committed holds should only be released in exceptional circumstances.
	HoldRelease(ctx context.Context, auth AuthContext, meta CommandMeta, req HoldReleaseRequest) (HoldReleaseResponse, error)

	// HoldExpire marks an expired pending hold (system-initiated, typically by background job).
	HoldExpire(ctx context.Context, auth AuthContext, meta CommandMeta, req HoldExpireRequest) (HoldExpireResponse, error)
}

// QuotePort defines the interface for quoting operations.
// Quoting is a pure function operation with no side effects or state changes.
type QuotePort interface {
	// EstimateQuote produces a non-binding price estimate.
	// This operation does NOT claim holds or modify any state.
	EstimateQuote(ctx context.Context, auth AuthContext, req EstimateQuoteRequest) (EstimateQuoteResponse, error)
}

// EntitlementPort defines the interface for customer entitlement operations.
// Entitlements can only be activated after a committed hold is in place.
type EntitlementPort interface {
	// ActivateEntitlement activates customer entitlement after committed reservation.
	// Preconditions:
	//   - Referenced hold must be in "committed" status
	//   - LegalSignoffRef must be present and valid
	ActivateEntitlement(ctx context.Context, auth AuthContext, meta CommandMeta, req ActivateEntitlementRequest) (ActivateEntitlementResponse, error)
}

// LedgerPort defines the interface for usage tracking and settlement operations.
// The ledger maintains an append-only immutable audit trail of usage and corrections.
type LedgerPort interface {
	// UsageHold places a hold on usage (pre-reservation before actual consumption).
	UsageHold(ctx context.Context, auth AuthContext, meta CommandMeta, req UsageHoldRequest) (UsageHoldResponse, error)

	// UsageSettle settles a held usage to a payable charge.
	// This state transition is irreversible. Once settled, the usage becomes
	// part of the billing cycle and cannot be modified (only corrected via UsageCorrect).
	UsageSettle(ctx context.Context, auth AuthContext, meta CommandMeta, req UsageSettleRequest) (UsageSettleResponse, error)

	// UsageCorrect appends a correction entry to the ledger (append-only, never modifies posted entries).
	UsageCorrect(ctx context.Context, auth AuthContext, meta CommandMeta, req UsageCorrectRequest) (UsageCorrectResponse, error)
}

// UnitOfWork defines the interface for transactional boundaries.
// Implementations coordinate multi-port operations within a single transaction.
type UnitOfWork interface {
	// Execute runs the given function within a transactional boundary.
	// All port operations within the function participate in the same transaction.
	// If the function returns an error, the transaction is rolled back.
	Execute(ctx context.Context, fn func(ctx context.Context) error) error
}
