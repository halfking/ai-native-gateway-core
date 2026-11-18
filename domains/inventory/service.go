package inventory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Service implements the InventoryPort interface for managing inventory and holds.
// It uses PostgreSQL for persistence with optimistic concurrency control.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new inventory service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// generateHoldID creates a unique hold identifier with hld_ prefix.
func generateHoldID() string {
	return fmt.Sprintf("hld_%d_%s", time.Now().UnixNano(), randomHex(8))
}

// generateFencingToken creates a cryptographically secure fencing token.
func generateFencingToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate fencing token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// randomHex generates a random hex string of the specified length.
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// QuerySellable returns the current snapshot of available sellable items.
// This is a read-only operation with no side effects.
func (s *Service) QuerySellable(
	ctx context.Context,
	auth maascontracts.AuthContext,
	req maascontracts.QuerySellableRequest,
) (maascontracts.QuerySellableResponse, error) {
	// Get the latest inventory snapshot
	var snapshotID string
	var revision int64
	var observedAt time.Time
	var stale bool

	err := s.pool.QueryRow(ctx, `
		SELECT snapshot_id, revision, observed_at, stale
		FROM maas_inventory_snapshots
		ORDER BY observed_at DESC
		LIMIT 1
	`).Scan(&snapshotID, &revision, &observedAt, &stale)

	if err == pgx.ErrNoRows {
		// No snapshot exists, return empty response
		return maascontracts.QuerySellableResponse{
			ResourceSnapshotID: fmt.Sprintf("snap_%d", time.Now().Unix()),
			InventoryRevision:  1,
			ObservedAt:         time.Now().UTC(),
			Stale:              false,
			Items:              []maascontracts.SellableItem{},
		}, nil
	}
	if err != nil {
		return maascontracts.QuerySellableResponse{}, fmt.Errorf("query latest snapshot: %w", err)
	}

	// Query sellable items for this snapshot
	rows, err := s.pool.Query(ctx, `
		SELECT model_sku, sellable_amount, sellable_unit, sellable_window,
		       capacity_available, shared_cap_id
		FROM maas_sellable_items
		WHERE snapshot_id = $1
		ORDER BY model_sku
	`, snapshotID)
	if err != nil {
		return maascontracts.QuerySellableResponse{}, fmt.Errorf("query sellable items: %w", err)
	}
	defer rows.Close()

	var items []maascontracts.SellableItem
	for rows.Next() {
		var item maascontracts.SellableItem
		var window *string
		var capAvail *bool
		var sharedCapID *string

		err := rows.Scan(
			&item.ModelSKU,
			&item.Sellable.Amount,
			&item.Sellable.Unit,
			&window,
			&capAvail,
			&sharedCapID,
		)
		if err != nil {
			return maascontracts.QuerySellableResponse{}, fmt.Errorf("scan sellable item: %w", err)
		}

		item.Sellable.Window = window
		item.Unit = item.Sellable.Unit
		item.CapacityAvailable = capAvail
		item.SharedCapID = sharedCapID

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return maascontracts.QuerySellableResponse{}, fmt.Errorf("iterate sellable items: %w", err)
	}

	return maascontracts.QuerySellableResponse{
		ResourceSnapshotID: snapshotID,
		InventoryRevision:  revision,
		ObservedAt:         observedAt,
		Stale:              stale,
		Items:              items,
	}, nil
}

// HoldCreate initiates a new pending reservation hold (soft reserve).
// The hold reserves inventory for a limited time period until expiration.
// UPDATED: Now includes anti-overselling logic by checking available inventory.
func (s *Service) HoldCreate(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	req maascontracts.HoldCreateRequest,
) (maascontracts.HoldCreateResponse, error) {
	// Check idempotency first
	cachedResp, err := s.checkIdempotency(ctx, auth.TenantID, "HoldCreate", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		// Idempotent replay - return cached response
		var resp maascontracts.HoldCreateResponse
		// TODO: Unmarshal cached response from JSONB
		return resp, nil
	}

	// Generate unique IDs
	holdID := generateHoldID()
	fencingToken, err := generateFencingToken()
	if err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("generate fencing token: %w", err)
	}

	// Get tenant ID from request or auth context
	tenantID := auth.TenantID
	if req.TenantID != nil && *req.TenantID != "" {
		// Verify tenant ID matches auth context
		if *req.TenantID != auth.TenantID {
			return maascontracts.HoldCreateResponse{}, &maascontracts.Error{
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
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// ========================================================================
	// ANTI-OVERSELLING LOGIC: Check inventory availability
	// ========================================================================
	for _, line := range req.Lines {
		// Get available inventory for this model
		var availableAmount string
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(sellable_amount::DECIMAL), 0)::TEXT
			FROM maas_sellable_items si
			JOIN maas_inventory_snapshots isn ON si.snapshot_id = isn.snapshot_id
			WHERE si.model_sku = $1
			  AND NOT isn.stale
		`, line.ModelSKU).Scan(&availableAmount)
		
		if err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("query available inventory: %w", err)
		}

		// Calculate already held quantity (pending + committed holds)
		var heldAmount string
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(hl.quantity_amount::DECIMAL), 0)::TEXT
			FROM maas_hold_lines hl
			JOIN maas_holds h ON hl.hold_id = h.hold_id
			WHERE hl.model_sku = $1
			  AND h.status IN ('pending', 'committed')
		`, line.ModelSKU).Scan(&heldAmount)
		
		if err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("query held inventory: %w", err)
		}

		// Check if requested amount + held amount > available
		available, _ := decimal.NewFromString(availableAmount)
		held, _ := decimal.NewFromString(heldAmount)
		requested, _ := decimal.NewFromString(line.Quantity.Amount)

		remaining := available.Sub(held)
		
		if requested.GreaterThan(remaining) {
			return maascontracts.HoldCreateResponse{}, &maascontracts.Error{
				Code:      "MAAS_INSUFFICIENT_INVENTORY",
				Message:   fmt.Sprintf("insufficient inventory for %s: requested %s, available %s", 
					line.ModelSKU, requested.String(), remaining.String()),
				Retryable: true, // User might retry later when inventory is available
			}
		}
	}

	// Get current inventory revision
	var inventoryRevision int64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision), 0) + 1
		FROM maas_inventory_snapshots
	`).Scan(&inventoryRevision)
	if err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("get inventory revision: %w", err)
	}

	// Insert hold record
	_, err = tx.Exec(ctx, `
		INSERT INTO maas_holds (
			hold_id, tenant_id, status, fencing_token, inventory_revision,
			quote_snapshot_id, resource_snapshot_id, expected_version,
			expires_at, idempotency_key, trace_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
	`,
		holdID,
		tenantID,
		string(maascontracts.HoldStatusPending),
		fencingToken,
		inventoryRevision,
		req.QuoteSnapshotID,
		req.ResourceSnapshotID,
		req.ExpectedVersion,
		req.ExpiresAt,
		meta.IdempotencyKey,
		auth.TraceID,
	)
	if err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("insert hold: %w", err)
	}

	// Insert hold lines
	for _, line := range req.Lines {
		_, err = tx.Exec(ctx, `
			INSERT INTO maas_hold_lines (
				hold_id, model_sku, quantity_amount, quantity_unit, quantity_window, shared_cap_id
			) VALUES ($1, $2, $3, $4, $5, $6)
		`,
			holdID,
			line.ModelSKU,
			line.Quantity.Amount,
			line.Quantity.Unit,
			line.Quantity.Window,
			line.SharedCapID,
		)
		if err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("insert hold line: %w", err)
		}
	}

	resp := maascontracts.HoldCreateResponse{
		HoldID:            holdID,
		Status:            maascontracts.HoldStatusPending,
		FencingToken:      fencingToken,
		InventoryRevision: inventoryRevision,
		ExpiresAt:         req.ExpiresAt,
	}

	// Record idempotency log
	err = s.recordIdempotency(ctx, tx, tenantID, "HoldCreate", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// HoldConfirm commits a pending hold to a hard reservation (committed state).
// This state transition is irreversible and decrements available inventory.
func (s *Service) HoldConfirm(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	holdID string,
	req maascontracts.HoldConfirmRequest,
) (maascontracts.HoldConfirmResponse, error) {
	// Check idempotency
	cachedResp, err := s.checkIdempotency(ctx, auth.TenantID, "HoldConfirm", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldConfirmResponse
		// TODO: Unmarshal cached response
		return resp, nil
	}

	// Start transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Query hold with row lock
	var status string
	var fencingToken string
	var expiresAt time.Time
	var inventoryRevision int64

	err = tx.QueryRow(ctx, `
		SELECT status, fencing_token, expires_at, inventory_revision
		FROM maas_holds
		WHERE hold_id = $1
		FOR UPDATE
	`, holdID).Scan(&status, &fencingToken, &expiresAt, &inventoryRevision)

	if err == pgx.ErrNoRows {
		return maascontracts.HoldConfirmResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorReserveNotCommitted,
			Message:   fmt.Sprintf("hold %s not found", holdID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("query hold: %w", err)
	}

	// Validate fencing token
	if fencingToken != req.FencingToken {
		return maascontracts.HoldConfirmResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorHoldFenceMismatch,
			Message:   "fencing token does not match",
			Retryable: false,
		}
	}

	// Validate status
	if status != string(maascontracts.HoldStatusPending) {
		return maascontracts.HoldConfirmResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorReserveNotCommitted,
			Message:   fmt.Sprintf("hold status is %s, expected pending", status),
			Retryable: false,
		}
	}

	// Check expiration
	now := time.Now().UTC()
	if now.After(expiresAt) {
		return maascontracts.HoldConfirmResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorHoldExpired,
			Message:   "hold has expired",
			Retryable: false,
		}
	}

	// Update hold status to committed
	_, err = tx.Exec(ctx, `
		UPDATE maas_holds
		SET status = $1, committed_at = $2, updated_at = $2
		WHERE hold_id = $3
	`, string(maascontracts.HoldStatusCommitted), now, holdID)
	if err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("update hold status: %w", err)
	}

	// TODO: Actually decrement inventory (hard reserve)
	// This requires updating inventory snapshots or a separate inventory balance table

	resp := maascontracts.HoldConfirmResponse{
		HoldID:            holdID,
		Status:            maascontracts.HoldStatusCommitted,
		InventoryRevision: inventoryRevision + 1,
		CommittedAt:       &now,
	}

	// Record idempotency
	err = s.recordIdempotency(ctx, tx, auth.TenantID, "HoldConfirm", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// HoldRelease releases a hold (pending or committed) back to inventory.
// Committed holds should only be released in exceptional circumstances.
func (s *Service) HoldRelease(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	holdID string,
	req maascontracts.HoldReleaseRequest,
) (maascontracts.HoldReleaseResponse, error) {
	// Check idempotency
	cachedResp, err := s.checkIdempotency(ctx, auth.TenantID, "HoldRelease", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldReleaseResponse
		// TODO: Unmarshal cached response
		return resp, nil
	}

	// Start transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Query hold with row lock
	var status string
	var fencingToken string

	err = tx.QueryRow(ctx, `
		SELECT status, fencing_token
		FROM maas_holds
		WHERE hold_id = $1
		FOR UPDATE
	`, holdID).Scan(&status, &fencingToken)

	if err == pgx.ErrNoRows {
		return maascontracts.HoldReleaseResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorReserveNotCommitted,
			Message:   fmt.Sprintf("hold %s not found", holdID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("query hold: %w", err)
	}

	// Validate fencing token if provided
	if req.FencingToken != nil && *req.FencingToken != fencingToken {
		return maascontracts.HoldReleaseResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorHoldFenceMismatch,
			Message:   "fencing token does not match",
			Retryable: false,
		}
	}

	// Check if already in terminal state
	currentStatus := maascontracts.HoldStatus(status)
	if currentStatus == maascontracts.HoldStatusAborted ||
		currentStatus == maascontracts.HoldStatusExpired ||
		currentStatus == maascontracts.HoldStatusCommitted {
		// Already terminal, return current status (idempotent)
		return maascontracts.HoldReleaseResponse{
			HoldID: holdID,
			Status: currentStatus,
		}, nil
	}

	// Update hold status to aborted
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE maas_holds
		SET status = $1, released_at = $2, updated_at = $2
		WHERE hold_id = $3
	`, string(maascontracts.HoldStatusAborted), now, holdID)
	if err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("update hold status: %w", err)
	}

	// TODO: Release inventory back (increment available quantity)

	resp := maascontracts.HoldReleaseResponse{
		HoldID: holdID,
		Status: maascontracts.HoldStatusAborted,
	}

	// Record idempotency
	err = s.recordIdempotency(ctx, tx, auth.TenantID, "HoldRelease", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// HoldExpire marks an expired hold (system-initiated or client-triggered).
// The inventory service has authoritative decision on expiration timing.
func (s *Service) HoldExpire(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	holdID string,
	req maascontracts.HoldExpireRequest,
) (maascontracts.HoldExpireResponse, error) {
	// Check idempotency
	cachedResp, err := s.checkIdempotency(ctx, auth.TenantID, "HoldExpire", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldExpireResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldExpireResponse
		// TODO: Unmarshal cached response
		return resp, nil
	}

	// Start transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return maascontracts.HoldExpireResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Query hold with row lock
	var status string
	var expiresAt time.Time

	err = tx.QueryRow(ctx, `
		SELECT status, expires_at
		FROM maas_holds
		WHERE hold_id = $1
		FOR UPDATE
	`, holdID).Scan(&status, &expiresAt)

	if err == pgx.ErrNoRows {
		return maascontracts.HoldExpireResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorReserveNotCommitted,
			Message:   fmt.Sprintf("hold %s not found", holdID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.HoldExpireResponse{}, fmt.Errorf("query hold: %w", err)
	}

	currentStatus := maascontracts.HoldStatus(status)
	now := time.Now().UTC()

	// Check if hold is actually expired (authoritative clock decision)
	isExpired := now.After(expiresAt) && currentStatus == maascontracts.HoldStatusPending

	// If not pending, return current status
	if currentStatus != maascontracts.HoldStatusPending {
		expired := currentStatus == maascontracts.HoldStatusExpired
		return maascontracts.HoldExpireResponse{
			HoldID:  holdID,
			Status:  currentStatus,
			Expired: &expired,
		}, nil
	}

	// If expired, update status
	if isExpired {
		_, err = tx.Exec(ctx, `
			UPDATE maas_holds
			SET status = $1, expired_at = $2, updated_at = $2
			WHERE hold_id = $3
		`, string(maascontracts.HoldStatusExpired), now, holdID)
		if err != nil {
			return maascontracts.HoldExpireResponse{}, fmt.Errorf("update hold status: %w", err)
		}

		// TODO: Release inventory back

		resp := maascontracts.HoldExpireResponse{
			HoldID:  holdID,
			Status:  maascontracts.HoldStatusExpired,
			Expired: &isExpired,
		}

		// Record idempotency
		err = s.recordIdempotency(ctx, tx, auth.TenantID, "HoldExpire", meta.IdempotencyKey, req, resp)
		if err != nil {
			return maascontracts.HoldExpireResponse{}, fmt.Errorf("record idempotency: %w", err)
		}

		// Commit transaction
		if err := tx.Commit(ctx); err != nil {
			return maascontracts.HoldExpireResponse{}, fmt.Errorf("commit transaction: %w", err)
		}

		return resp, nil
	}

	// Not expired yet, return pending status
	notExpired := false
	return maascontracts.HoldExpireResponse{
		HoldID:  holdID,
		Status:  maascontracts.HoldStatusPending,
		Expired: &notExpired,
	}, nil
}

// checkIdempotency checks if this command has already been executed.
// Returns the cached response if found, nil otherwise.
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
		return nil, nil // No cached response
	}
	if err != nil {
		return nil, fmt.Errorf("query idempotency log: %w", err)
	}

	// TODO: Unmarshal response based on command type
	return responsePayload, nil
}

// recordIdempotency records the command execution for future idempotency checks.
func (s *Service) recordIdempotency(ctx context.Context, tx pgx.Tx, tenantID, commandType, idempotencyKey string, request, response interface{}) error {
	// TODO: Marshal request and response to JSONB
	// For now, use empty JSONB
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour) // 7 days TTL

	_, err := tx.Exec(ctx, `
		INSERT INTO maas_idempotency_log (
			tenant_id, command_type, idempotency_key,
			request_payload, response_payload, status, executed_at, expires_at
		) VALUES ($1, $2, $3, '{}'::jsonb, '{}'::jsonb, 'success', NOW(), $4)
		ON CONFLICT (tenant_id, command_type, idempotency_key) DO NOTHING
	`, tenantID, commandType, idempotencyKey, expiresAt)

	return err
}

// Verify that Service implements InventoryPort interface at compile time.
var _ maascontracts.InventoryPort = (*Service)(nil)
