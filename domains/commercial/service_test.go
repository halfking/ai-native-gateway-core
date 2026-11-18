package commercial_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/commercial"
)

func TestEstimateQuote_Success(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL with pricing data")

	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
		Scopes:   []string{"maas.quote.estimate"},
	}

	priceVersionID := "prv_2026_11_initial"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000000", Unit: "token"},
		ResourceSnapshotID: "snap_001",
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
}

func TestEstimateQuote_HoldNotCreated(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// Critical test: verify EstimateQuote never creates a hold
	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &priceVersionID,
	}

	resp, err := svc.EstimateQuote(ctx, auth, req)
	require.NoError(t, err)

	// This is the core contract requirement
	assert.False(t, resp.HoldCreated, "Contract violation: EstimateQuote created a hold")

	// Additionally verify no hold exists in database
	// TODO: Query maas_holds table to ensure no hold was created
}

func TestEstimateQuote_WithPercentageDiscount(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL with pricing rules")

	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	// Quantity > 10M triggers 10% discount in seed data
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "15000000", Unit: "token"},
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &priceVersionID,
	}

	resp, err := svc.EstimateQuote(ctx, auth, req)
	require.NoError(t, err)

	// Base price: 15M tokens * $0.03/1M = $450
	// With 10% discount: $450 * 0.9 = $405
	// In minor units (cents): 40500
	expectedMinor := "40500"
	assert.Equal(t, expectedMinor, resp.Money.AmountMinor)
}

func TestEstimateQuote_VolumeTier(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL with tiered pricing")

	// Test that higher volume gets lower unit price
	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"

	// Small volume
	smallReq := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000000", Unit: "token"}, // 1M
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &priceVersionID,
	}

	smallResp, err := svc.EstimateQuote(ctx, auth, smallReq)
	require.NoError(t, err)

	// Large volume
	largeReq := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "100000000", Unit: "token"}, // 100M
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &priceVersionID,
	}

	largeResp, err := svc.EstimateQuote(ctx, auth, largeReq)
	require.NoError(t, err)

	// Calculate unit price
	smallUnitPrice := parseDecimal(smallResp.Money.AmountMinor) / 1.0 // 1M tokens
	largeUnitPrice := parseDecimal(largeResp.Money.AmountMinor) / 100.0 // 100M tokens

	// Volume discount should result in lower unit price
	assert.Less(t, largeUnitPrice, smallUnitPrice, "Large volume should have lower unit price")
}

func TestEstimateQuote_NoPriceRule(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	// Non-existent model
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "non_existent_model",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &priceVersionID,
	}

	_, err := svc.EstimateQuote(ctx, auth, req)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, "MAAS_PRICE_RULE_NOT_FOUND", maasErr.Code)
}

func TestEstimateQuote_ExpiredPriceVersion(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	// Request an old, expired price version
	expiredVersionID := "prv_2025_01_expired"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &expiredVersionID,
	}

	_, err := svc.EstimateQuote(ctx, auth, req)

	// Should either error or succeed with expired version
	// (business rule decision)
	if err != nil {
		maasErr, ok := err.(*maascontracts.Error)
		require.True(t, ok)
		assert.Contains(t, maasErr.Message, "expired")
	}
}

func TestEstimateQuote_Idempotent(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// EstimateQuote is stateless but should be safe to call multiple times
	svc := commercial.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	priceVersionID := "prv_2026_11_initial"
	req := maascontracts.EstimateQuoteRequest{
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ResourceSnapshotID: "snap_001",
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
