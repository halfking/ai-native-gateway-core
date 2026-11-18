package inventory

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Service implements the InventoryPort interface for managing inventory and holds.
// It uses PostgreSQL for persistence with optimistic concurrency control.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new inventory service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// QuerySellable returns the current snapshot of available sellable items.
// This is a read-only operation with no side effects.
func (s *Service) QuerySellable(
	ctx context.Context,
	auth maascontracts.AuthContext,
	req maascontracts.QuerySellableRequest,
) (maascontracts.QuerySellableResponse, error) {
	// TODO: Implement inventory snapshot query
	// For now, return a placeholder response
	return maascontracts.QuerySellableResponse{
		ResourceSnapshotID: fmt.Sprintf("snap_%d", time.Now().Unix()),
		InventoryRevision:  1,
		ObservedAt:         time.Now().UTC(),
		Stale:              false,
		Items:              []maascontracts.SellableItem{},
	}, nil
}

// HoldCreate initiates a new pending reservation hold (soft reserve).
// The hold reserves inventory for a limited time period until expiration.
func (s *Service) HoldCreate(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	req maascontracts.HoldCreateRequest,
) (maascontracts.HoldCreateResponse, error) {
	// TODO: Implement hold creation with inventory decrement
	// For now, return a placeholder response
	holdID := fmt.Sprintf("hld_%d", time.Now().UnixNano())
	fencingToken := fmt.Sprintf("fence_%s", holdID)
	
	return maascontracts.HoldCreateResponse{
		HoldID:            holdID,
		Status:            maascontracts.HoldStatusPending,
		FencingToken:      fencingToken,
		InventoryRevision: 2,
		ExpiresAt:         req.ExpiresAt,
	}, nil
}

// HoldConfirm commits a pending hold to a hard reservation (committed state).
// This state transition is irreversible and decrements available inventory.
func (s *Service) HoldConfirm(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	holdID string,
	req maascontracts.HoldConfirmRequest,
) (maascontracts.HoldConfirmResponse, error) {
	// TODO: Implement hold confirmation with fence token validation
	now := time.Now().UTC()
	
	return maascontracts.HoldConfirmResponse{
		HoldID:            holdID,
		Status:            maascontracts.HoldStatusCommitted,
		InventoryRevision: 3,
		CommittedAt:       &now,
	}, nil
}

// HoldRelease releases a hold (pending or committed) back to inventory.
// Committed holds should only be released in exceptional circumstances.
func (s *Service) HoldRelease(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	holdID string,
	req maascontracts.HoldReleaseRequest,
) (maascontracts.HoldReleaseResponse, error) {
	// TODO: Implement hold release with inventory increment
	return maascontracts.HoldReleaseResponse{
		HoldID: holdID,
		Status: maascontracts.HoldStatusAborted,
	}, nil
}

// HoldExpire marks an expired hold (system-initiated or client-triggered).
// The inventory service has authoritative decision on expiration timing.
func (s *Service) HoldExpire(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	holdID string,
	req maascontracts.HoldExpireRequest,
) (maascontracts.HoldExpireResponse, error) {
	// TODO: Implement hold expiration
	expired := true
	
	return maascontracts.HoldExpireResponse{
		HoldID:  holdID,
		Status:  maascontracts.HoldStatusExpired,
		Expired: &expired,
	}, nil
}

// Verify that Service implements InventoryPort interface at compile time.
var _ maascontracts.InventoryPort = (*Service)(nil)
