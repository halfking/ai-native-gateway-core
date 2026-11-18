package commercial

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Service implements the QuotePort interface for price estimation.
// Quoting is a pure function operation with no side effects or state changes.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new commercial/quoting service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// EstimateQuote produces a non-binding price quote.
// This operation does NOT create holds or modify any state.
// Per contract v0.1: EstimateQuote MUST NOT create an inventory hold.
func (s *Service) EstimateQuote(
	ctx context.Context,
	auth maascontracts.AuthContext,
	req maascontracts.EstimateQuoteRequest,
) (maascontracts.EstimateQuoteResponse, error) {
	// TODO: Implement actual price calculation logic
	// For now, return a placeholder response with zero cost
	
	quoteID := fmt.Sprintf("quo_%d", time.Now().UnixNano())
	quoteSnapshotID := fmt.Sprintf("qsnap_%d", time.Now().UnixNano())
	
	// Placeholder: actual pricing logic will query price tables
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	
	return maascontracts.EstimateQuoteResponse{
		QuoteID:            quoteID,
		QuoteSnapshotID:    quoteSnapshotID,
		PriceVersionID:     *req.PriceVersionID,
		ResourceSnapshotID: req.ResourceSnapshotID,
		Status:             maascontracts.QuoteStatusReference,
		Money: maascontracts.Money{
			AmountMinor: "0",      // Placeholder: will calculate from price tables
			Currency:    "USD",
		},
		ExpiresAt:   &expiresAt,
		HoldCreated: false, // MUST be false per contract v0.1
	}, nil
}

// Verify that Service implements QuotePort interface at compile time.
var _ maascontracts.QuotePort = (*Service)(nil)
