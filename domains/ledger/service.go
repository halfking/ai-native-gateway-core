package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Service implements the LedgerPort interface for usage tracking and settlement.
// The ledger maintains an append-only immutable audit trail of usage and corrections.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new ledger service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// generateUsageHoldID creates a unique usage hold identifier with usg_ prefix.
func generateUsageHoldID() string {
	return fmt.Sprintf("usg_%d_%s", time.Now().UnixNano(), randomHex(8))
}

// generateReversalID creates a unique reversal identifier.
func generateReversalID() string {
	return fmt.Sprintf("rev_%d_%s", time.Now().UnixNano(), randomHex(8))
}

// randomHex generates a random hex string of the specified length.
func randomHex(n int) string {
	return fmt.Sprintf("%x", time.Now().UnixNano())[:n]
}

// UsageHold places a hold on usage against an activated entitlement.
// This is a request-level hold, not a second decrement of inventory hold H.
func (s *Service) UsageHold(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	req maascontracts.UsageHoldRequest,
) (maascontracts.UsageHoldResponse, error) {
	// Check idempotency using request_id as natural idempotency key
	var existingUsageHoldID string
	var existingStatus string
	var existingExpiresAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT usage_hold_id, status, expires_at
		FROM maas_usage_holds
		WHERE request_id = $1 AND tenant_id = $2
		LIMIT 1
	`, req.RequestID, auth.TenantID).Scan(&existingUsageHoldID, &existingStatus, &existingExpiresAt)

	if err == nil {
		// Usage hold already exists for this request (idempotent)
		return maascontracts.UsageHoldResponse{
			UsageHoldID: existingUsageHoldID,
			Status:      maascontracts.UsageStatus(existingStatus),
			ExpiresAt:   existingExpiresAt,
		}, nil
	} else if err != pgx.ErrNoRows {
		return maascontracts.UsageHoldResponse{}, fmt.Errorf("check existing usage hold: %w", err)
	}

	// Get tenant ID from request or auth context
	tenantID := auth.TenantID
	if req.TenantID != nil && *req.TenantID != "" {
		if *req.TenantID != auth.TenantID {
			return maascontracts.UsageHoldResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorUnauthorizedScope,
				Message:   "tenant_id in request does not match authenticated tenant",
				Retryable: false,
			}
		}
		tenantID = *req.TenantID
	}

	// Start transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.UsageHoldResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Verify entitlement exists and is active
	var entitlementStatus string
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM maas_entitlements
		WHERE entitlement_id = $1 AND tenant_id = $2
		FOR UPDATE
	`, req.EntitlementID, tenantID).Scan(&entitlementStatus)

	if err == pgx.ErrNoRows {
		return maascontracts.UsageHoldResponse{}, &maascontracts.Error{
			Code:      "MAAS_ENTITLEMENT_NOT_FOUND",
			Message:   fmt.Sprintf("entitlement %s not found", req.EntitlementID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.UsageHoldResponse{}, fmt.Errorf("query entitlement: %w", err)
	}

	// Verify entitlement is active
	if entitlementStatus != string(maascontracts.EntitlementStatusActive) {
		return maascontracts.UsageHoldResponse{}, &maascontracts.Error{
			Code:      "MAAS_ENTITLEMENT_NOT_ACTIVE",
			Message:   fmt.Sprintf("entitlement status is %s, expected active", entitlementStatus),
			Retryable: false,
		}
	}

	// ========================================================================
	// QUOTA TRACKING: Check if entitlement has sufficient quota
	// ========================================================================
	if req.ModelSKU != nil && *req.ModelSKU != "" {
		var quotaAvailable bool
		err = tx.QueryRow(ctx, `
			SELECT maas_check_quota_available($1, $2, $3)
		`, req.EntitlementID, *req.ModelSKU, req.Quantity.Amount).Scan(&quotaAvailable)
		
		if err != nil {
			return maascontracts.UsageHoldResponse{}, fmt.Errorf("check quota: %w", err)
		}

		if !quotaAvailable {
			return maascontracts.UsageHoldResponse{}, &maascontracts.Error{
				Code:      "MAAS_QUOTA_EXCEEDED",
				Message:   fmt.Sprintf("insufficient quota for %s", *req.ModelSKU),
				Retryable: false,
			}
		}

		// Increment held quota
		_, err = tx.Exec(ctx, `
			UPDATE maas_entitlement_quotas
			SET 
				held_amount = (held_amount::DECIMAL + $1::DECIMAL)::TEXT,
				quota_version = quota_version + 1,
				updated_at = NOW()
			WHERE entitlement_id = $2 AND model_sku = $3
		`, req.Quantity.Amount, req.EntitlementID, *req.ModelSKU)
		
		if err != nil {
			return maascontracts.UsageHoldResponse{}, fmt.Errorf("increment held quota: %w", err)
		}
	}

	if err == pgx.ErrNoRows {
		return maascontracts.UsageHoldResponse{}, &maascontracts.Error{
			Code:      "MAAS_ENTITLEMENT_NOT_FOUND",
			Message:   fmt.Sprintf("entitlement %s not found", req.EntitlementID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.UsageHoldResponse{}, fmt.Errorf("query entitlement: %w", err)
	}

	// Verify entitlement is active
	if entitlementStatus != string(maascontracts.EntitlementStatusActive) {
		return maascontracts.UsageHoldResponse{}, &maascontracts.Error{
			Code:      "MAAS_ENTITLEMENT_NOT_ACTIVE",
			Message:   fmt.Sprintf("entitlement status is %s, expected active", entitlementStatus),
			Retryable: false,
		}
	}

	// TODO: Check if entitlement has sufficient quota remaining
	// This requires tracking consumed vs. allocated quantities

	// Generate usage hold ID
	usageHoldID := generateUsageHoldID()
	expiresAt := time.Now().UTC().Add(5 * time.Minute) // Default 5 minute hold

	// Insert usage hold record
	_, err = tx.Exec(ctx, `
		INSERT INTO maas_usage_holds (
			usage_hold_id, tenant_id, entitlement_id, request_id,
			quantity_amount, quantity_unit, model_sku, status,
			expires_at, idempotency_key, trace_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
	`,
		usageHoldID,
		tenantID,
		req.EntitlementID,
		req.RequestID,
		req.Quantity.Amount,
		req.Quantity.Unit,
		req.ModelSKU,
		string(maascontracts.UsageStatusHeld),
		expiresAt,
		meta.IdempotencyKey,
		auth.TraceID,
	)
	if err != nil {
		return maascontracts.UsageHoldResponse{}, fmt.Errorf("insert usage hold: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.UsageHoldResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return maascontracts.UsageHoldResponse{
		UsageHoldID: usageHoldID,
		Status:      maascontracts.UsageStatusHeld,
		ExpiresAt:   &expiresAt,
	}, nil
}

// UsageSettle settles actual usage and releases unused remainder.
// This state transition is irreversible. Once settled, the usage becomes
// part of the billing cycle and cannot be modified (only corrected via UsageCorrect).
func (s *Service) UsageSettle(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	usageHoldID string,
	req maascontracts.UsageSettleRequest,
) (maascontracts.UsageSettleResponse, error) {
	// Check idempotency
	cachedResp, err := s.checkIdempotency(ctx, auth.TenantID, "UsageSettle", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.UsageSettleResponse
		// TODO: Unmarshal cached response
		return resp, nil
	}

	// Get tenant ID
	tenantID := auth.TenantID
	if req.TenantID != nil && *req.TenantID != "" {
		if *req.TenantID != auth.TenantID {
			return maascontracts.UsageSettleResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorUnauthorizedScope,
				Message:   "tenant_id in request does not match authenticated tenant",
				Retryable: false,
			}
		}
		tenantID = *req.TenantID
	}

	// Start transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Query usage hold with row lock
	var status string
	var heldAmount, heldUnit string
	var entitlementID string
	var holdTenantID string
	var modelSKU *string

	err = tx.QueryRow(ctx, `
		SELECT status, quantity_amount, quantity_unit, entitlement_id, tenant_id, model_sku
		FROM maas_usage_holds
		WHERE usage_hold_id = $1
		FOR UPDATE
	`, usageHoldID).Scan(&status, &heldAmount, &heldUnit, &entitlementID, &holdTenantID, &modelSKU)

	if err == pgx.ErrNoRows {
		return maascontracts.UsageSettleResponse{}, &maascontracts.Error{
			Code:      "MAAS_USAGE_HOLD_NOT_FOUND",
			Message:   fmt.Sprintf("usage hold %s not found", usageHoldID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("query usage hold: %w", err)
	}

	// Verify tenant matches
	if holdTenantID != tenantID {
		return maascontracts.UsageSettleResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorUnauthorizedScope,
			Message:   "usage hold belongs to a different tenant",
			Retryable: false,
		}
	}

	// Verify usage hold is in held status
	if status != string(maascontracts.UsageStatusHeld) {
		if status == string(maascontracts.UsageStatusSettled) {
			return maascontracts.UsageSettleResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorUsageAlreadySettled,
				Message:   "usage hold already settled",
				Retryable: false,
			}
		}
		return maascontracts.UsageSettleResponse{}, &maascontracts.Error{
			Code:      "MAAS_INVALID_USAGE_STATUS",
			Message:   fmt.Sprintf("usage hold status is %s, expected held", status),
			Retryable: false,
		}
	}

	// Calculate released amount: held - actual
	held, err := decimal.NewFromString(heldAmount)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("parse held amount: %w", err)
	}

	actual, err := decimal.NewFromString(req.Actual.Amount)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("parse actual amount: %w", err)
	}

	released := held.Sub(actual)
	if released.LessThan(decimal.Zero) {
		// Actual usage exceeds hold (rare, but possible in streaming)
		released = decimal.Zero
	}

	// ========================================================================
	// QUOTA SETTLEMENT: Move from held to consumed
	// ========================================================================
	if modelSKU != nil && *modelSKU != "" {
		_, err = tx.Exec(ctx, `
			UPDATE maas_entitlement_quotas
			SET 
				held_amount = (held_amount::DECIMAL - $1::DECIMAL)::TEXT,
				consumed_amount = (consumed_amount::DECIMAL + $2::DECIMAL)::TEXT,
				quota_version = quota_version + 1,
				updated_at = NOW()
			WHERE entitlement_id = $3 AND model_sku = $4
		`, heldAmount, req.Actual.Amount, entitlementID, *modelSKU)
		
		if err != nil {
			return maascontracts.UsageSettleResponse{}, fmt.Errorf("settle quota: %w", err)
		}
	}

	// Update usage hold status to settled
	_, err = tx.Exec(ctx, `
		UPDATE maas_usage_holds
		SET status = $1, updated_at = NOW()
		WHERE usage_hold_id = $2
	`, string(maascontracts.UsageStatusSettled), usageHoldID)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("update usage hold: %w", err)
	}

	// Insert settlement record
	entryID := fmt.Sprintf("entry_%d", time.Now().UnixNano())
	_, err = tx.Exec(ctx, `
		INSERT INTO maas_usage_settlements (
			usage_hold_id, settled_amount, settled_unit,
			released_amount, released_unit, stream_interrupted, entry_id, settled_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`,
		usageHoldID,
		req.Actual.Amount,
		req.Actual.Unit,
		released.String(),
		heldUnit,
		req.StreamInterrupted,
		entryID,
	)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("insert settlement: %w", err)
	}

	resp := maascontracts.UsageSettleResponse{
		UsageHoldID: usageHoldID,
		Status:      maascontracts.UsageStatusSettled,
		Settled:     req.Actual,
		Released: maascontracts.Quantity{
			Amount: released.String(),
			Unit:   heldUnit,
		},
		EntryID: &entryID,
	}

	// Record idempotency
	err = s.recordIdempotency(ctx, tx, tenantID, "UsageSettle", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// UsageCorrect appends a correction entry to the ledger (append-only, never modifies posted entries).
// This creates a reversal entry for audit trail without modifying the original entry.
func (s *Service) UsageCorrect(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	req maascontracts.UsageCorrectRequest,
) (maascontracts.UsageCorrectResponse, error) {
	// Check idempotency
	cachedResp, err := s.checkIdempotency(ctx, auth.TenantID, "UsageCorrect", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.UsageCorrectResponse
		// TODO: Unmarshal cached response
		return resp, nil
	}

	// Get tenant ID
	tenantID := auth.TenantID
	if req.TenantID != nil && *req.TenantID != "" {
		if *req.TenantID != auth.TenantID {
			return maascontracts.UsageCorrectResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorUnauthorizedScope,
				Message:   "tenant_id in request does not match authenticated tenant",
				Retryable: false,
			}
		}
		tenantID = *req.TenantID
	}

	// Start transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Verify entry exists (we don't lock it, as corrections are append-only)
	var entryExists bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM maas_usage_settlements WHERE entry_id = $1
		)
	`, req.EntryID).Scan(&entryExists)

	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("check entry exists: %w", err)
	}

	if !entryExists {
		return maascontracts.UsageCorrectResponse{}, &maascontracts.Error{
			Code:      "MAAS_ENTRY_NOT_FOUND",
			Message:   fmt.Sprintf("ledger entry %s not found", req.EntryID),
			Retryable: false,
		}
	}

	// Generate reversal ID
	reversalID := generateReversalID()

	// Insert correction record (append-only)
	_, err = tx.Exec(ctx, `
		INSERT INTO maas_usage_corrections (
			reversal_id, tenant_id, entry_id,
			reversal_amount, reversal_unit, reason, status,
			idempotency_key, trace_id, corrected_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'posted', $7, $8, NOW())
	`,
		reversalID,
		tenantID,
		req.EntryID,
		req.Reversal.Amount,
		req.Reversal.Unit,
		req.Reason,
		meta.IdempotencyKey,
		auth.TraceID,
	)
	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("insert correction: %w", err)
	}

	status := maascontracts.UsageStatusPosted
	resp := maascontracts.UsageCorrectResponse{
		ReversalID: reversalID,
		EntryID:    req.EntryID,
		Status:     &status,
	}

	// Record idempotency
	err = s.recordIdempotency(ctx, tx, tenantID, "UsageCorrect", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// checkIdempotency checks if this command has already been executed.
func (s *Service) checkIdempotency(ctx context.Context, tenantID, commandType, idempotencyKey string) (interface{}, error) {
	var responsePayload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT response_payload
		FROM maas_idempotency_log
		WHERE tenant_id = $1 AND command_type = $2 AND idempotency_key = $3
		AND status = 'success'
		AND expires_at > NOW()
	`, tenantID, commandType, idempotencyKey).Scan(&responsePayload)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query idempotency log: %w", err)
	}

	return responsePayload, nil
}

// recordIdempotency records the command execution for future idempotency checks.
func (s *Service) recordIdempotency(ctx context.Context, tx pgx.Tx, tenantID, commandType, idempotencyKey string, request, response interface{}) error {
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)

	_, err := tx.Exec(ctx, `
		INSERT INTO maas_idempotency_log (
			tenant_id, command_type, idempotency_key,
			request_payload, response_payload, status, executed_at, expires_at
		) VALUES ($1, $2, $3, '{}'::jsonb, '{}'::jsonb, 'success', NOW(), $4)
		ON CONFLICT (tenant_id, command_type, idempotency_key) DO NOTHING
	`, tenantID, commandType, idempotencyKey, expiresAt)

	return err
}

// Verify that Service implements LedgerPort interface at compile time.
var _ maascontracts.LedgerPort = (*Service)(nil)
