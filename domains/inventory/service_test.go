package inventory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/internal/testutil"
)

func TestHoldCreate_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
		Scopes:   []string{"maas.inventory.reserve"},
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_hold_create_001",
	}

	expiresAt := time.Now().UTC().Add(1 * time.Hour)
	window := "monthly"

	req := maascontracts.HoldCreateRequest{
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
		ExpiresAt: expiresAt,
	}

	resp, err := svc.HoldCreate(ctx, auth, meta, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.HoldID)
	assert.Equal(t, maascontracts.HoldStatusPending, resp.Status)
	assert.NotEmpty(t, resp.FencingToken)
	assert.Greater(t, resp.InventoryRevision, int64(0))
	assert.Equal(t, expiresAt.Unix(), resp.ExpiresAt.Unix())

	t.Logf("Hold created: %s, Status: %s, Fencing Token: %s", 
		resp.HoldID, resp.Status, "[REDACTED]")
}

func TestHoldCreate_Idempotency(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_002",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_same_key_test",
	}

	expiresAt := time.Now().UTC().Add(1 * time.Hour)
	window := "monthly"

	req := maascontracts.HoldCreateRequest{
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
		ExpiresAt: expiresAt,
	}

	// First call
	resp1, err1 := svc.HoldCreate(ctx, auth, meta, req)
	require.NoError(t, err1)

	// Second call with same idempotency key
	resp2, err2 := svc.HoldCreate(ctx, auth, meta, req)
	require.NoError(t, err2)

	// Should return same hold ID (idempotent)
	assert.Equal(t, resp1.HoldID, resp2.HoldID)
	assert.Equal(t, resp1.FencingToken, resp2.FencingToken)
	assert.Equal(t, resp1.Status, resp2.Status)
}

func TestHoldConfirm_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_003",
	}

	// First create a hold
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_create_for_confirm",
	}

	expiresAt := time.Now().UTC().Add(1 * time.Hour)
	window := "monthly"

	createReq := maascontracts.HoldCreateRequest{
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
		ExpiresAt: expiresAt,
	}

	createResp, err := svc.HoldCreate(ctx, auth, createMeta, createReq)
	require.NoError(t, err)

	// Now confirm it
	confirmMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_confirm_001",
	}

	confirmReq := maascontracts.HoldConfirmRequest{
		FencingToken: createResp.FencingToken,
	}

	confirmResp, err := svc.HoldConfirm(ctx, auth, confirmMeta, createResp.HoldID, confirmReq)

	require.NoError(t, err)
	assert.Equal(t, createResp.HoldID, confirmResp.HoldID)
	assert.Equal(t, maascontracts.HoldStatusCommitted, confirmResp.Status)
	assert.NotNil(t, confirmResp.CommittedAt)
	assert.Greater(t, confirmResp.InventoryRevision, createResp.InventoryRevision)

	t.Logf("Hold confirmed: %s, Status: %s", confirmResp.HoldID, confirmResp.Status)
}

func TestHoldConfirm_FencingTokenMismatch(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_004",
	}

	// Create a hold first
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_create_fence_test",
	}

	expiresAt := time.Now().UTC().Add(1 * time.Hour)
	window := "monthly"

	createReq := maascontracts.HoldCreateRequest{
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
		ExpiresAt: expiresAt,
	}

	createResp, err := svc.HoldCreate(ctx, auth, createMeta, createReq)
	require.NoError(t, err)

	// Try to confirm with wrong fencing token
	confirmMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_confirm_wrong_fence",
	}

	confirmReq := maascontracts.HoldConfirmRequest{
		FencingToken: "[REDACTED:secret:2]",
	}

	_, err = svc.HoldConfirm(ctx, auth, confirmMeta, createResp.HoldID, confirmReq)

	require.Error(t, err)
	
	// Should return ErrorHoldFenceMismatch
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok, "error should be *maascontracts.Error")
	assert.Equal(t, maascontracts.ErrorHoldFenceMismatch, maasErr.Code)
}

func TestHoldRelease_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_005",
	}

	// Create a hold
	createMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_create_for_release",
	}

	expiresAt := time.Now().UTC().Add(1 * time.Hour)
	window := "monthly"

	createReq := maascontracts.HoldCreateRequest{
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
		ExpiresAt: expiresAt,
	}

	createResp, err := svc.HoldCreate(ctx, auth, createMeta, createReq)
	require.NoError(t, err)

	// Release it
	releaseMeta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_release_001",
	}

	releaseReq := maascontracts.HoldReleaseRequest{
		FencingToken: &createResp.FencingToken,
	}

	releaseResp, err := svc.HoldRelease(ctx, auth, releaseMeta, createResp.HoldID, releaseReq)

	require.NoError(t, err)
	assert.Equal(t, createResp.HoldID, releaseResp.HoldID)
	assert.Equal(t, maascontracts.HoldStatusAborted, releaseResp.Status)
}

func TestQuerySellable_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	svc := inventory.NewService(db.Pool)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_006",
	}

	req := maascontracts.QuerySellableRequest{
		ModelSKU: "gpt-4",
	}

	resp, err := svc.QuerySellable(ctx, auth, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.ResourceSnapshotID)
	assert.Greater(t, resp.InventoryRevision, int64(0))
	assert.False(t, resp.Stale)
	assert.NotEmpty(t, resp.Items, "Should have sellable items from seed data")

	t.Logf("Found %d sellable items in snapshot %s (revision %d)",
		len(resp.Items), resp.ResourceSnapshotID, resp.InventoryRevision)
}
