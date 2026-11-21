package e2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/commercial"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/domains/ledger"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
)

// TestE2E_CompletePurchaseAndConsumptionFlow tests the full end-to-end flow:
// Query → Quote → Hold → Confirm → Activate → Usage Hold → Usage Settle
func TestE2E_CompletePurchaseAndConsumptionFlow(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL with complete schema and seed data")

	// Setup services (in real implementation, use dependency injection)
	var (
		inventorySvc  = inventory.NewService(nil)
		commercialSvc = commercial.NewService(nil)
		tradingSvc    = trading.NewService(nil)
		ledgerSvc     = ledger.NewService(nil)
	)

	ctx := context.Background()
	auth := maascontracts.AuthContext{
		TenantID: "ten_test_e2e",
		Scopes:   []string{"maas.*"},
		TraceID:  "trace_e2e_001", // string 类型（contracts.AuthContext.TraceID string，原 ptrString 编译不过）
	}

	// ========================================================================
	// Step 1: Query Available Inventory
	// ========================================================================
	t.Log("Step 1: QuerySellable")

	queryReq := maascontracts.QuerySellableRequest{
		ModelSKU: "gpt-4",
		TenantID: &auth.TenantID,
	}

	queryResp, err := inventorySvc.QuerySellable(ctx, auth, queryReq)
	require.NoError(t, err)
	require.NotEmpty(t, queryResp.Items, "No inventory available")

	t.Logf("  ✓ Found %d sellable items", len(queryResp.Items))
	t.Logf("  ✓ Snapshot: %s, Revision: %d", queryResp.ResourceSnapshotID, queryResp.InventoryRevision)

	// ========================================================================
	// Step 2: Get Price Quote
	// ========================================================================
	t.Log("Step 2: EstimateQuote")

	priceVersionID := "prv_2026_11_initial"
	window := "monthly"
	quoteReq := maascontracts.EstimateQuoteRequest{
		TenantID:           &auth.TenantID,
		ModelSKU:           "gpt-4",
		Quantity:           maascontracts.Quantity{Amount: "1000000", Unit: "token", Window: &window},
		ResourceSnapshotID: queryResp.ResourceSnapshotID,
		PriceVersionID:     &priceVersionID,
	}

	quoteResp, err := commercialSvc.EstimateQuote(ctx, auth, quoteReq)
	require.NoError(t, err)
	assert.False(t, quoteResp.HoldCreated, "Contract violation: Quote created a hold")

	t.Logf("  ✓ Quote: %s, Price: %s %s", quoteResp.QuoteID, quoteResp.Money.AmountMinor, quoteResp.Money.Currency)
	t.Logf("  ✓ HoldCreated: %v (must be false)", quoteResp.HoldCreated)

	// ========================================================================
	// Step 3: Create Hold (Pending Reservation)
	// ========================================================================
	t.Log("Step 3: HoldCreate")

	holdCreateMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_e2e_hold_create",
	}

	holdCreateReq := maascontracts.HoldCreateRequest{
		TenantID:           &auth.TenantID,
		QuoteSnapshotID:    &quoteResp.QuoteSnapshotID,
		ResourceSnapshotID: &queryResp.ResourceSnapshotID,
		Lines: []maascontracts.HoldLine{
			{
				ModelSKU: "gpt-4",
				Quantity: maascontracts.Quantity{Amount: "1000000", Unit: "token", Window: &window},
			},
		},
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}

	holdCreateResp, err := inventorySvc.HoldCreate(ctx, auth, holdCreateMeta, holdCreateReq)
	require.NoError(t, err)
	assert.Equal(t, maascontracts.HoldStatusPending, holdCreateResp.Status)

	t.Logf("  ✓ Hold: %s, Status: %s", holdCreateResp.HoldID, holdCreateResp.Status)
	t.Logf("  ✓ Fencing Token: %s", holdCreateResp.FencingToken)
	t.Logf("  ✓ Expires: %s", holdCreateResp.ExpiresAt)

	// ========================================================================
	// Step 4: Confirm Hold (Committed Reservation)
	// ========================================================================
	t.Log("Step 4: HoldConfirm")

	holdConfirmMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_e2e_hold_confirm",
	}

	holdConfirmReq := maascontracts.HoldConfirmRequest{
		TenantID:        &auth.TenantID,
		FencingToken:    holdCreateResp.FencingToken,
		QuoteSnapshotID: &quoteResp.QuoteSnapshotID,
	}

	holdConfirmResp, err := inventorySvc.HoldConfirm(ctx, auth, holdConfirmMeta, holdCreateResp.HoldID, holdConfirmReq)
	require.NoError(t, err)
	assert.Equal(t, maascontracts.HoldStatusCommitted, holdConfirmResp.Status)

	t.Logf("  ✓ Hold: %s, Status: %s", holdConfirmResp.HoldID, holdConfirmResp.Status)
	t.Logf("  ✓ Committed At: %s", holdConfirmResp.CommittedAt)

	// ========================================================================
	// Step 5: Activate Entitlement
	// ========================================================================
	t.Log("Step 5: ActivateEntitlement")

	activateMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_e2e_activate",
	}

	legalRef := "legal_e2e_001"
	contractID := "contract_e2e_001"
	activateReq := maascontracts.ActivateEntitlementRequest{
		TenantID:        &auth.TenantID,
		HoldID:          holdConfirmResp.HoldID,
		LegalSignoffRef: &legalRef,
		ContractID:      &contractID,
	}

	activateResp, err := tradingSvc.ActivateEntitlement(ctx, auth, activateMeta, activateReq)
	require.NoError(t, err)
	assert.Equal(t, maascontracts.EntitlementStatusActive, activateResp.Status)

	t.Logf("  ✓ Entitlement: %s, Status: %s", activateResp.EntitlementID, activateResp.Status)
	t.Logf("  ✓ Activated At: %s", activateResp.ActivatedAt)

	// ========================================================================
	// Step 6: Usage Hold (Request-level)
	// ========================================================================
	t.Log("Step 6: UsageHold")

	usageHoldMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_e2e_usage_hold",
	}

	modelSKU := "gpt-4"
	usageHoldReq := maascontracts.UsageHoldRequest{
		TenantID:      &auth.TenantID,
		EntitlementID: activateResp.EntitlementID,
		RequestID:     "req_e2e_001",
		Quantity:      maascontracts.Quantity{Amount: "10000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	usageHoldResp, err := ledgerSvc.UsageHold(ctx, auth, usageHoldMeta, usageHoldReq)
	require.NoError(t, err)
	assert.Equal(t, maascontracts.UsageStatusHeld, usageHoldResp.Status)

	t.Logf("  ✓ Usage Hold: %s, Status: %s", usageHoldResp.UsageHoldID, usageHoldResp.Status)

	// ========================================================================
	// Step 7: Usage Settle (Actual Consumption)
	// ========================================================================
	t.Log("Step 7: UsageSettle")

	usageSettleMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_e2e_usage_settle",
	}

	usageSettleReq := maascontracts.UsageSettleRequest{
		TenantID:          &auth.TenantID,
		Actual:            maascontracts.Quantity{Amount: "8500", Unit: "token"},
		StreamInterrupted: false,
	}

	usageSettleResp, err := ledgerSvc.UsageSettle(ctx, auth, usageSettleMeta, usageHoldResp.UsageHoldID, usageSettleReq)
	require.NoError(t, err)
	assert.Equal(t, maascontracts.UsageStatusSettled, usageSettleResp.Status)

	t.Logf("  ✓ Usage Settled: %s", usageSettleResp.UsageHoldID)
	t.Logf("  ✓ Settled: %s %s", usageSettleResp.Settled.Amount, usageSettleResp.Settled.Unit)
	t.Logf("  ✓ Released: %s %s", usageSettleResp.Released.Amount, usageSettleResp.Released.Unit)
	t.Logf("  ✓ Entry ID: %s", *usageSettleResp.EntryID)

	// ========================================================================
	// Step 8: Verify Balance
	// ========================================================================
	t.Log("Step 8: Verify Balance")

	held := parseDecimal("10000")
	settled := parseDecimal(usageSettleResp.Settled.Amount)
	released := parseDecimal(usageSettleResp.Released.Amount)

	assert.Equal(t, held, settled+released, "Balance check failed: held != settled + released")
	t.Logf("  ✓ Balance verified: %g = %g + %g", held, settled, released)

	// ========================================================================
	// Complete!
	// ========================================================================
	t.Log("✅ End-to-end flow completed successfully")
}

func parseDecimal(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}
