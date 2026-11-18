package ledger_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/ledger"
)

func TestUsageHold_Success(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
		Scopes:   []string{"maas.usage.hold"},
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_usage_hold_001",
	}

	modelSKU := "gpt-4"
	req := maascontracts.UsageHoldRequest{
		EntitlementID: "ent_active_001",
		RequestID:     "req_unique_001",
		Quantity:      maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	resp, err := svc.UsageHold(ctx, auth, meta, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.UsageHoldID)
	assert.Equal(t, maascontracts.UsageStatusHeld, resp.Status)
	assert.NotNil(t, resp.ExpiresAt)
}

func TestUsageHold_EntitlementNotActive(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_usage_hold_suspended",
	}

	// Try to hold against a suspended entitlement
	req := maascontracts.UsageHoldRequest{
		EntitlementID: "ent_suspended_001",
		RequestID:     "req_unique_002",
		Quantity:      maascontracts.Quantity{Amount: "1000", Unit: "token"},
	}

	_, err := svc.UsageHold(ctx, auth, meta, req)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, "MAAS_ENTITLEMENT_NOT_ACTIVE", maasErr.Code)
}

func TestUsageHold_Idempotent(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// Test natural idempotency using request_id
	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	modelSKU := "gpt-4"
	req := maascontracts.UsageHoldRequest{
		EntitlementID: "ent_active_002",
		RequestID:     "req_same_id",
		Quantity:      maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	// First hold
	meta1 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_1",
	}
	resp1, err1 := svc.UsageHold(ctx, auth, meta1, req)
	require.NoError(t, err1)

	// Second hold with SAME request_id
	meta2 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_2",
	}
	resp2, err2 := svc.UsageHold(ctx, auth, meta2, req)
	require.NoError(t, err2)

	// Should return SAME usage hold (natural idempotency via request_id)
	assert.Equal(t, resp1.UsageHoldID, resp2.UsageHoldID)
}

func TestUsageSettle_Success(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	// First create a usage hold
	holdMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_hold_for_settle",
	}

	modelSKU := "gpt-4"
	holdReq := maascontracts.UsageHoldRequest{
		EntitlementID: "ent_active_003",
		RequestID:     "req_for_settle",
		Quantity:      maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	holdResp, err := svc.UsageHold(ctx, auth, holdMeta, holdReq)
	require.NoError(t, err)

	// Now settle it
	settleMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_001",
	}

	settleReq := maascontracts.UsageSettleRequest{
		Actual:            maascontracts.Quantity{Amount: "750", Unit: "token"},
		StreamInterrupted: false,
	}

	settleResp, err := svc.UsageSettle(ctx, auth, settleMeta, holdResp.UsageHoldID, settleReq)

	require.NoError(t, err)
	assert.Equal(t, holdResp.UsageHoldID, settleResp.UsageHoldID)
	assert.Equal(t, maascontracts.UsageStatusSettled, settleResp.Status)
	assert.Equal(t, "750", settleResp.Settled.Amount)
	assert.Equal(t, "250", settleResp.Released.Amount) // 1000 - 750 = 250
	assert.NotNil(t, settleResp.EntryID)
}

func TestUsageSettle_BalanceCheck(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// Critical test: verify settled + released = held
	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	// Create hold for 1000 tokens
	holdMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_hold_balance",
	}

	modelSKU := "gpt-4"
	holdReq := maascontracts.UsageHoldRequest{
		EntitlementID: "ent_active_004",
		RequestID:     "req_balance_test",
		Quantity:      maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	holdResp, err := svc.UsageHold(ctx, auth, holdMeta, holdReq)
	require.NoError(t, err)

	// Settle with 850 tokens
	settleMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_balance",
	}

	settleReq := maascontracts.UsageSettleRequest{
		Actual: maascontracts.Quantity{Amount: "850", Unit: "token"},
	}

	settleResp, err := svc.UsageSettle(ctx, auth, settleMeta, holdResp.UsageHoldID, settleReq)
	require.NoError(t, err)

	// Verify balance: settled + released = held
	held := parseDecimal("1000")
	settled := parseDecimal(settleResp.Settled.Amount)
	released := parseDecimal(settleResp.Released.Amount)

	assert.Equal(t, held, settled+released, "Balance check failed: settled + released != held")
}

func TestUsageSettle_AlreadySettled(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	// Create and settle a usage hold
	holdMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_hold_double_settle",
	}

	modelSKU := "gpt-4"
	holdReq := maascontracts.UsageHoldRequest{
		EntitlementID: "ent_active_005",
		RequestID:     "req_double_settle",
		Quantity:      maascontracts.Quantity{Amount: "1000", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	holdResp, err := svc.UsageHold(ctx, auth, holdMeta, holdReq)
	require.NoError(t, err)

	// First settle
	settleMeta1 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_first",
	}

	settleReq := maascontracts.UsageSettleRequest{
		Actual: maascontracts.Quantity{Amount: "750", Unit: "token"},
	}

	_, err = svc.UsageSettle(ctx, auth, settleMeta1, holdResp.UsageHoldID, settleReq)
	require.NoError(t, err)

	// Try to settle again
	settleMeta2 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_settle_second",
	}

	_, err = svc.UsageSettle(ctx, auth, settleMeta2, holdResp.UsageHoldID, settleReq)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, maascontracts.ErrorUsageAlreadySettled, maasErr.Code)
}

func TestUsageCorrect_Success(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_correct_001",
	}

	reason := "billing adjustment"
	req := maascontracts.UsageCorrectRequest{
		EntryID:  "entry_settled_001",
		Reversal: maascontracts.Quantity{Amount: "-50", Unit: "token"},
		Reason:   &reason,
	}

	resp, err := svc.UsageCorrect(ctx, auth, meta, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.ReversalID)
	assert.Equal(t, req.EntryID, resp.EntryID)
	assert.NotNil(t, resp.Status)
	assert.Equal(t, maascontracts.UsageStatusPosted, *resp.Status)
}

func TestUsageCorrect_EntryNotFound(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_correct_notfound",
	}

	req := maascontracts.UsageCorrectRequest{
		EntryID:  "entry_nonexistent",
		Reversal: maascontracts.Quantity{Amount: "-10", Unit: "token"},
	}

	_, err := svc.UsageCorrect(ctx, auth, meta, req)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, "MAAS_ENTRY_NOT_FOUND", maasErr.Code)
}

func TestUsageCorrect_AppendOnly(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// Critical test: verify corrections are append-only (don't modify original entry)
	svc := ledger.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	// Create, settle, then correct
	// ... (setup code)

	// Apply correction
	correctMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_correct_appendonly",
	}

	reason := "test append-only"
	correctReq := maascontracts.UsageCorrectRequest{
		EntryID:  "entry_to_correct",
		Reversal: maascontracts.Quantity{Amount: "-100", Unit: "token"},
		Reason:   &reason,
	}

	correctResp, err := svc.UsageCorrect(ctx, auth, correctMeta, correctReq)
	require.NoError(t, err)

	// Verify original entry unchanged
	// TODO: Query maas_usage_settlements to verify original entry is unchanged
	// Verify correction entry exists in maas_usage_corrections
	assert.NotEmpty(t, correctResp.ReversalID)
}

// Helper function
func parseDecimal(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}
