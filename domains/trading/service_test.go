package trading_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
	"github.com/kaixuan/llm-gateway-go/internal/testutil"
)

func TestActivateEntitlement_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tradingSvc := trading.NewService(db.Pool)
	inventorySvc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_trade_001",
		Scopes:   []string{"maas.entitlement.activate"},
	}

	// First create and confirm a hold
	window := "monthly"
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_hold_create",
	}
	holdReq := maascontracts.HoldCreateRequest{
		Lines: []maascontracts.HoldLine{
			{
				ModelSKU: "gpt-4",
				Quantity: maascontracts.Quantity{
					Amount: "1000",
					Unit:   "token",
					Window: &window,
				},
			},
		},
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}

	holdResp, err := inventorySvc.HoldCreate(ctx, auth, createMeta, holdReq)
	require.NoError(t, err)

	// Confirm the hold
	confirmMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_hold_confirm",
	}
	confirmReq := maascontracts.HoldConfirmRequest{
		FencingToken: holdResp.FencingToken,
	}
	confirmResp, err := inventorySvc.HoldConfirm(ctx, auth, confirmMeta, holdResp.HoldID, confirmReq)
	require.NoError(t, err)

	// Now activate entitlement
	activateMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_001",
	}

	legalRef := "legal_trade_001"
	contractID := "contract_trade_001"
	activateReq := maascontracts.ActivateEntitlementRequest{
		HoldID:          confirmResp.HoldID,
		LegalSignoffRef: &legalRef,
		ContractID:      &contractID,
	}

	activateResp, err := tradingSvc.ActivateEntitlement(ctx, auth, activateMeta, activateReq)

	require.NoError(t, err)
	assert.NotEmpty(t, activateResp.EntitlementID)
	assert.Equal(t, maascontracts.EntitlementStatusActive, activateResp.Status)
	assert.Equal(t, confirmResp.HoldID, activateResp.HoldID)
	assert.NotNil(t, activateResp.ActivatedAt)

	t.Logf("Entitlement activated: %s, Status: %s", activateResp.EntitlementID, activateResp.Status)
}

func TestActivateEntitlement_HoldNotCommitted(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tradingSvc := trading.NewService(db.Pool)
	inventorySvc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_trade_002",
	}

	// Create a hold but don't confirm it
	window := "monthly"
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_pending_hold",
	}
	holdReq := maascontracts.HoldCreateRequest{
		Lines: []maascontracts.HoldLine{
			{
				ModelSKU: "gpt-4",
				Quantity: maascontracts.Quantity{
					Amount: "1000",
					Unit:   "token",
					Window: &window,
				},
			},
		},
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}

	holdResp, err := inventorySvc.HoldCreate(ctx, auth, createMeta, holdReq)
	require.NoError(t, err)

	// Try to activate with pending hold
	activateMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_pending",
	}
	activateReq := maascontracts.ActivateEntitlementRequest{
		HoldID: holdResp.HoldID,
	}

	_, err = tradingSvc.ActivateEntitlement(ctx, auth, activateMeta, activateReq)

	require.Error(t, err)

	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, maascontracts.ErrorReserveNotCommitted, maasErr.Code)
}

func TestActivateEntitlement_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tradingSvc := trading.NewService(db.Pool)
	inventorySvc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_trade_003",
	}

	// Create and confirm a hold
	window := "monthly"
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_idem_create",
	}
	holdReq := maascontracts.HoldCreateRequest{
		Lines: []maascontracts.HoldLine{
			{
				ModelSKU: "gpt-4",
				Quantity: maascontracts.Quantity{
					Amount: "1000",
					Unit:   "token",
					Window: &window,
				},
			},
		},
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}

	holdResp, err := inventorySvc.HoldCreate(ctx, auth, createMeta, holdReq)
	require.NoError(t, err)

	confirmMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_idem_confirm",
	}
	confirmReq := maascontracts.HoldConfirmRequest{
		FencingToken: holdResp.FencingToken,
	}
	confirmResp, err := inventorySvc.HoldConfirm(ctx, auth, confirmMeta, holdResp.HoldID, confirmReq)
	require.NoError(t, err)

	// Activate with same idempotency key twice
	activateMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_same",
	}
	activateReq := maascontracts.ActivateEntitlementRequest{
		HoldID: confirmResp.HoldID,
	}

	// First activation
	resp1, err1 := tradingSvc.ActivateEntitlement(ctx, auth, activateMeta, activateReq)
	require.NoError(t, err1)

	// Second activation with same idempotency key (should be idempotent)
	resp2, err2 := tradingSvc.ActivateEntitlement(ctx, auth, activateMeta, activateReq)
	require.NoError(t, err2)

	// Should return same entitlement
	assert.Equal(t, resp1.EntitlementID, resp2.EntitlementID)
	assert.Equal(t, resp1.Status, resp2.Status)
}

func TestActivateEntitlement_DuplicateHold(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tradingSvc := trading.NewService(db.Pool)
	inventorySvc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_trade_004",
	}

	// Create and confirm a hold
	window := "monthly"
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_dup_create",
	}
	holdReq := maascontracts.HoldCreateRequest{
		Lines: []maascontracts.HoldLine{
			{
				ModelSKU: "gpt-4",
				Quantity: maascontracts.Quantity{
					Amount: "1000",
					Unit:   "token",
					Window: &window,
				},
			},
		},
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}

	holdResp, err := inventorySvc.HoldCreate(ctx, auth, createMeta, holdReq)
	require.NoError(t, err)

	confirmMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_trade_dup_confirm",
	}
	confirmReq := maascontracts.HoldConfirmRequest{
		FencingToken: holdResp.FencingToken,
	}
	confirmResp, err := inventorySvc.HoldConfirm(ctx, auth, confirmMeta, holdResp.HoldID, confirmReq)
	require.NoError(t, err)

	activateReq := maascontracts.ActivateEntitlementRequest{
		HoldID: confirmResp.HoldID,
	}

	// First activation
	meta1 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_dup1",
	}
	resp1, err1 := tradingSvc.ActivateEntitlement(ctx, auth, meta1, activateReq)
	require.NoError(t, err1)

	// Second activation with DIFFERENT idempotency key but SAME hold
	// Should return the SAME entitlement (data-level idempotency)
	meta2 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_dup2",
	}
	resp2, err2 := tradingSvc.ActivateEntitlement(ctx, auth, meta2, activateReq)
	require.NoError(t, err2)

	assert.Equal(t, resp1.EntitlementID, resp2.EntitlementID)
}
