package trading_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
)

func TestActivateEntitlement_Success(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := trading.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
		Scopes:   []string{"maas.entitlement.activate"},
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_001",
	}

	legalRef := "legal_signoff_abc123"
	contractID := "contract_xyz789"

	req := maascontracts.ActivateEntitlementRequest{
		HoldID:          "hld_committed_001",
		LegalSignoffRef: &legalRef,
		ContractID:      &contractID,
	}

	resp, err := svc.ActivateEntitlement(ctx, auth, meta, req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.EntitlementID)
	assert.Equal(t, maascontracts.EntitlementStatusActive, resp.Status)
	assert.Equal(t, req.HoldID, resp.HoldID)
	assert.NotNil(t, resp.ActivatedAt)
}

func TestActivateEntitlement_HoldNotCommitted(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := trading.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_pending",
	}

	// Try to activate with a hold that's still pending
	req := maascontracts.ActivateEntitlementRequest{
		HoldID: "hld_pending_001",
	}

	_, err := svc.ActivateEntitlement(ctx, auth, meta, req)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, maascontracts.ErrorReserveNotCommitted, maasErr.Code)
}

func TestActivateEntitlement_HoldNotFound(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := trading.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_notfound",
	}

	req := maascontracts.ActivateEntitlementRequest{
		HoldID: "hld_nonexistent",
	}

	_, err := svc.ActivateEntitlement(ctx, auth, meta, req)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, maascontracts.ErrorReserveNotCommitted, maasErr.Code)
}

func TestActivateEntitlement_TenantMismatch(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	svc := trading.NewService(nil)
	ctx := context.Background()

	// User from tenant A tries to activate hold from tenant B
	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_wrongtenant",
	}

	req := maascontracts.ActivateEntitlementRequest{
		HoldID: "hld_tenant_b_hold",
	}

	_, err := svc.ActivateEntitlement(ctx, auth, meta, req)

	require.Error(t, err)
	
	maasErr, ok := err.(*maascontracts.Error)
	require.True(t, ok)
	assert.Equal(t, maascontracts.ErrorUnauthorizedScope, maasErr.Code)
}

func TestActivateEntitlement_Idempotent(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// Test that activating the same hold twice returns the same entitlement
	svc := trading.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	meta := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_same",
	}

	req := maascontracts.ActivateEntitlementRequest{
		HoldID: "hld_committed_002",
	}

	// First activation
	resp1, err1 := svc.ActivateEntitlement(ctx, auth, meta, req)
	require.NoError(t, err1)

	// Second activation with same idempotency key
	resp2, err2 := svc.ActivateEntitlement(ctx, auth, meta, req)
	require.NoError(t, err2)

	// Should return same entitlement
	assert.Equal(t, resp1.EntitlementID, resp2.EntitlementID)
	assert.Equal(t, resp1.Status, resp2.Status)
}

func TestActivateEntitlement_DuplicateHold(t *testing.T) {
	t.Skip("Requires testcontainers-go + PostgreSQL")

	// Test data-level idempotency: same hold can't create multiple entitlements
	svc := trading.NewService(nil)
	ctx := context.Background()

	auth := maascontracts.AuthContext{
		TenantID: "ten_test_001",
	}

	req := maascontracts.ActivateEntitlementRequest{
		HoldID: "hld_committed_003",
	}

	// First activation
	meta1 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_dup1",
	}
	resp1, err1 := svc.ActivateEntitlement(ctx, auth, meta1, req)
	require.NoError(t, err1)

	// Second activation with DIFFERENT idempotency key but SAME hold
	meta2 := maascontracts.CommandMeta{
		IdempotencyKey: "idem_activate_dup2",
	}
	resp2, err2 := svc.ActivateEntitlement(ctx, auth, meta2, req)
	require.NoError(t, err2)

	// Should return the SAME entitlement (data-level idempotency)
	assert.Equal(t, resp1.EntitlementID, resp2.EntitlementID)
}
