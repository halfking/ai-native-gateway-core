package ledger_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/domains/ledger"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
	"github.com/kaixuan/llm-gateway-go/internal/testutil"
)

// setupEntitlementWithQuota creates a test entitlement with quota
func setupEntitlementWithQuota(t *testing.T, ctx context.Context, db *testutil.TestDatabase, tenantID string) string {
	t.Helper()

	inventorySvc := inventory.NewService(db.Pool)
	tradingSvc := trading.NewService(db.Pool)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Create and confirm a hold
	window := "monthly"
	holdResp, err := inventorySvc.HoldCreate(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: fmt.Sprintf("idem_ledger_setup_%s", tenantID),
	}, maascontracts.HoldCreateRequest{
		Lines: []maascontracts.HoldLine{{
			ModelSKU: "gpt-4",
			Quantity: maascontracts.Quantity{Amount: "1000000", Unit: "token", Window: &window},
		}},
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	})
	require.NoError(t, err)

	confirmResp, err := inventorySvc.HoldConfirm(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: fmt.Sprintf("idem_ledger_confirm_%s", tenantID),
	}, holdResp.HoldID, maascontracts.HoldConfirmRequest{
		FencingToken: holdResp.FencingToken,
	})
	require.NoError(t, err)

	// Activate entitlement
	activateResp, err := tradingSvc.ActivateEntitlement(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: fmt.Sprintf("idem_ledger_activate_%s", tenantID),
	}, maascontracts.ActivateEntitlementRequest{
		HoldID: confirmResp.HoldID,
	})
	require.NoError(t, err)

	// Create quota record
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO maas_entitlement_quotas (
			entitlement_id, model_sku, 
			allocated_amount, allocated_unit,
			consumed_unit, held_unit
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (entitlement_id, model_sku) DO NOTHING
	`, activateResp.EntitlementID, "gpt-4", "1000000", "token", "token", "token")
	require.NoError(t, err)

	return activateResp.EntitlementID
}

func TestUsageHold_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_001"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
		Scopes:   []string{"maas.usage.hold"},
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_usage_hold_001",
	}

	modelSKU := "gpt-4"
	req := maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_unique_001",
		Quantity:      maascontracts.Quantity{Amount: "10000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	resp, err := svc.UsageHold(ctx, auth, meta, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.UsageHoldID)
	assert.Equal(t, maascontracts.UsageStatusHeld, resp.Status)
	assert.NotNil(t, resp.ExpiresAt)

	// Verify quota was held
	var heldAmount string
	err = db.Pool.QueryRow(ctx, `
		SELECT held_amount 
		FROM maas_entitlement_quotas 
		WHERE entitlement_id = $1 AND model_sku = $2
	`, entitlementID, modelSKU).Scan(&heldAmount)
	require.NoError(t, err)
	assert.Equal(t, "10000", heldAmount)

	t.Logf("Usage hold created: %s, Held quota: %s", resp.UsageHoldID, heldAmount)
}

func TestUsageHold_QuotaExceeded(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_002"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Try to hold more than allocated (1M allocated, request 2M)
	modelSKU := "gpt-4"
	req := maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_exceed_001",
		Quantity:      maascontracts.Quantity{Amount: "2000000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	_, err := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_quota_exceed",
	}, req)

	require.Error(t, err)

	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, "MAAS_QUOTA_EXCEEDED", maasErr.Code)
}

func TestUsageSettle_Success_WithQuotaTracking(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_003"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Create a usage hold
	modelSKU := "gpt-4"
	holdResp, err := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_hold",
	}, maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_settle_001",
		Quantity:      maascontracts.Quantity{Amount: "10000", Unit: "token"},
		ModelSKU:      &modelSKU,
	})
	require.NoError(t, err)

	// Verify held quota
	var heldBefore string
	err = db.Pool.QueryRow(ctx, `
		SELECT held_amount FROM maas_entitlement_quotas 
		WHERE entitlement_id = $1 AND model_sku = $2
	`, entitlementID, modelSKU).Scan(&heldBefore)
	require.NoError(t, err)
	assert.Equal(t, "10000", heldBefore)

	// Now settle (actual usage: 8500 tokens)
	settleResp, err := svc.UsageSettle(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_001",
	}, holdResp.UsageHoldID, maascontracts.UsageSettleRequest{
		Actual:            maascontracts.Quantity{Amount: "8500", Unit: "token"},
		StreamInterrupted: false,
	})

	require.NoError(t, err)
	assert.Equal(t, holdResp.UsageHoldID, settleResp.UsageHoldID)
	assert.Equal(t, maascontracts.UsageStatusSettled, settleResp.Status)
	assert.Equal(t, "8500", settleResp.Settled.Amount)
	assert.Equal(t, "1500", settleResp.Released.Amount)
	assert.NotNil(t, settleResp.EntryID)

	// Verify balance: settled + released = held
	held := parseDecimal("10000")
	settled := parseDecimal(settleResp.Settled.Amount)
	released := parseDecimal(settleResp.Released.Amount)
	assert.Equal(t, held, settled+released, "Balance check failed")

	// Verify quota was settled (held → consumed)
	var heldAfter, consumedAfter string
	err = db.Pool.QueryRow(ctx, `
		SELECT held_amount, consumed_amount 
		FROM maas_entitlement_quotas 
		WHERE entitlement_id = $1 AND model_sku = $2
	`, entitlementID, modelSKU).Scan(&heldAfter, &consumedAfter)
	require.NoError(t, err)

	assert.Equal(t, "0", heldAfter, "Held amount should be 0 after settle")
	assert.Equal(t, "8500", consumedAfter, "Consumed should equal settled amount")

	t.Logf("Quota after settle - Held: %s, Consumed: %s", heldAfter, consumedAfter)
}

func TestUsageSettle_BalanceCheck(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_004"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Create hold for 10000 tokens
	modelSKU := "gpt-4"
	holdResp, err := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_balance_hold",
	}, maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_balance_001",
		Quantity:      maascontracts.Quantity{Amount: "10000", Unit: "token"},
		ModelSKU:      &modelSKU,
	})
	require.NoError(t, err)

	// Settle with 8500 tokens
	settleResp, err := svc.UsageSettle(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_balance_settle",
	}, holdResp.UsageHoldID, maascontracts.UsageSettleRequest{
		Actual: maascontracts.Quantity{Amount: "8500", Unit: "token"},
	})
	require.NoError(t, err)

	// Verify balance: settled + released = held
	held := parseDecimal("10000")
	settled := parseDecimal(settleResp.Settled.Amount)
	released := parseDecimal(settleResp.Released.Amount)

	assert.Equal(t, held, settled+released, "Balance check failed: settled + released != held")
	t.Logf("Balance verified: %.0f = %.0f + %.0f", held, settled, released)
}

func TestUsageSettle_AlreadySettled(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_005"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Create and settle a usage hold
	modelSKU := "gpt-4"
	holdResp, err := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_double_settle_hold",
	}, maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_double_001",
		Quantity:      maascontracts.Quantity{Amount: "10000", Unit: "token"},
		ModelSKU:      &modelSKU,
	})
	require.NoError(t, err)

	// First settle
	_, err = svc.UsageSettle(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_first",
	}, holdResp.UsageHoldID, maascontracts.UsageSettleRequest{
		Actual: maascontracts.Quantity{Amount: "7500", Unit: "token"},
	})
	require.NoError(t, err)

	// Try to settle again
	_, err = svc.UsageSettle(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_second",
	}, holdResp.UsageHoldID, maascontracts.UsageSettleRequest{
		Actual: maascontracts.Quantity{Amount: "5000", Unit: "token"},
	})

	require.Error(t, err)

	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, maascontracts.ErrorUsageAlreadySettled, maasErr.Code)
}

func TestUsageCorrect_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_006"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Create, hold and settle
	modelSKU := "gpt-4"
	holdResp, err := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_correct_hold",
	}, maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_correct_001",
		Quantity:      maascontracts.Quantity{Amount: "10000", Unit: "token"},
		ModelSKU:      &modelSKU,
	})
	require.NoError(t, err)

	settleResp, err := svc.UsageSettle(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_correct_settle",
	}, holdResp.UsageHoldID, maascontracts.UsageSettleRequest{
		Actual: maascontracts.Quantity{Amount: "8000", Unit: "token"},
	})
	require.NoError(t, err)

	// Now apply correction
	reason := "billing adjustment"
	correctResp, err := svc.UsageCorrect(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_correct_001",
	}, maascontracts.UsageCorrectRequest{
		EntryID:  *settleResp.EntryID,
		Reversal: maascontracts.Quantity{Amount: "-500", Unit: "token"},
		Reason:   &reason,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, correctResp.ReversalID)
	assert.Equal(t, *settleResp.EntryID, correctResp.EntryID)
	assert.NotNil(t, correctResp.Status)
	assert.Equal(t, maascontracts.UsageStatusPosted, *correctResp.Status)

	t.Logf("Correction created: %s for entry %s", correctResp.ReversalID, correctResp.EntryID)
}

func TestUsageHold_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := ledger.NewService(db.Pool)
	ctx := context.Background()

	tenantID := "ten_ledger_007"
	entitlementID := setupEntitlementWithQuota(t, ctx, db, tenantID)

	auth := maascontracts.AuthContext{
		TenantID: tenantID,
	}

	// Test natural idempotency using request_id
	modelSKU := "gpt-4"
	req := maascontracts.UsageHoldRequest{
		EntitlementID: entitlementID,
		RequestID:     "req_same_id_test",
		Quantity:      maascontracts.Quantity{Amount: "5000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	// First hold
	resp1, err1 := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_1",
	}, req)
	require.NoError(t, err1)

	// Second hold with SAME request_id (natural idempotency)
	resp2, err2 := svc.UsageHold(ctx, auth, maascontracts.CommandMeta{
		IdempotencyKey: "idem_2",
	}, req)
	require.NoError(t, err2)

	// Should return SAME usage hold
	assert.Equal(t, resp1.UsageHoldID, resp2.UsageHoldID)

	// Verify quota was only held once
	var heldAmount string
	err := db.Pool.QueryRow(ctx, `
		SELECT held_amount FROM maas_entitlement_quotas 
		WHERE entitlement_id = $1 AND model_sku = $2
	`, entitlementID, modelSKU).Scan(&heldAmount)
	require.NoError(t, err)
	assert.Equal(t, "5000", heldAmount, "Quota should only be held once")
}

// Helper function
func parseDecimal(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}
