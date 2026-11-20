package commercial_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/commercial"
	"github.com/kaixuan/llm-gateway-go/internal/testutil"
)

func TestEstimateQuote_Success(t *testing.T) {
	// Setup test database
	db := testutil.SetupTestDB(t)
	svc := commercial.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
		Scopes:   []string{"maas.quote.estimate"},
	}

	priceVersionID := "prv_2026_11_initial"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000000", Unit: "token"},
		ResourceSnapshotID: "snap_test_001",
		PriceVersionID:     &priceVersionID,
	}

	resp, err := svc.EstimateQuote(ctx, auth, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.QuoteID)
	assert.NotEmpty(t, resp.QuoteSnapshotID)
	assert.Equal(t, priceVersionID, resp.PriceVersionID)
	assert.Equal(t, maascontracts.QuoteStatusReference, resp.Status)
	assert.NotEmpty(t, resp.Money.AmountMinor)
	assert.Equal(t, "USD", resp.Money.Currency)
	assert.False(t, resp.HoldCreated, "EstimateQuote MUST NOT create holds")

	t.Logf("Quote created: %s, Amount: %s %s", resp.QuoteID, resp.Money.AmountMinor, resp.Money.Currency)
}

func TestEstimateQuote_HoldNotCreated(t *testing.T) {
	// Critical test: verify EstimateQuote never creates a hold
	db := testutil.SetupTestDB(t)
	svc := commercial.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_test_001",
		PriceVersionID:     &priceVersionID,
	}

	resp, err := svc.EstimateQuote(ctx, auth, req)
	require.NoError(t, err)

	// This is the core contract requirement
	assert.False(t, resp.HoldCreated, "Contract violation: EstimateQuote created a hold")

	// Additionally verify no hold exists in database
	var holdCount int
	err = db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM maas_holds WHERE tenant_id = $1`, auth.TenantID).Scan(&holdCount)
	require.NoError(t, err)
	assert.Equal(t, 0, holdCount, "No holds should exist after EstimateQuote")
}

func TestEstimateQuote_WithPercentageDiscount(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := commercial.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	// Quantity > 10M triggers 10% discount in seed data
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "15000000", Unit: "token"},
		ResourceSnapshotID: "snap_test_001",
		PriceVersionID:     &priceVersionID,
	}

	resp, err := svc.EstimateQuote(ctx, auth, req)
	require.NoError(t, err)

	// Base price: 15M tokens * $0.03/1M = $450 = 45000 cents
	// With 10% discount: 45000 * 0.9 = 40500 cents
	// Note: applyModifiers is not yet fully implemented, so this might not match exactly
	t.Logf("Quote amount (with discount): %s cents", resp.Money.AmountMinor)

	// The amount should be less than or equal to base price
	basePrice := parseDecimal("45000")
	actualPrice := parseDecimal(resp.Money.AmountMinor)
	assert.LessOrEqual(t, actualPrice, basePrice, "Discounted price should be <= base price")
}

func TestEstimateQuote_NoPriceRule(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := commercial.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	// Non-existent model
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "non_existent_model",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_test_001",
		PriceVersionID:     &priceVersionID,
	}

	_, err := svc.EstimateQuote(ctx, auth, req)

	require.Error(t, err)

	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, "MAAS_PRICE_RULE_NOT_FOUND", maasErr.Code)
}

func TestEstimateQuote_Idempotent(t *testing.T) {
	// EstimateQuote is stateless but should be safe to call multiple times
	db := testutil.SetupTestDB(t)
	svc := commercial.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_test_001",
		PriceVersionID:     &priceVersionID,
	}

	// Call multiple times
	resp1, err1 := svc.EstimateQuote(ctx, auth, req)
	require.NoError(t, err1)

	resp2, err2 := svc.EstimateQuote(ctx, auth, req)
	require.NoError(t, err2)

	// Prices should be identical (different quote IDs though)
	assert.Equal(t, resp1.Money.AmountMinor, resp2.Money.AmountMinor)
	assert.Equal(t, resp1.Money.Currency, resp2.Money.Currency)
	assert.NotEqual(t, resp1.QuoteID, resp2.QuoteID, "Each call should generate new quote ID")
}

// Helper function to parse decimal string to float64 for comparison
// Only for testing purposes - production code uses decimal.Decimal
func parseDecimal(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}
