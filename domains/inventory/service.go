package inventory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/internal/maasidem"
)

// Service implements the InventoryPort interface for managing inventory and holds.
// It uses PostgreSQL for persistence with optimistic concurrency control.
//
// ★ D7 / D9（2026-11-19 审计轮）：pool 由具体类型放宽为窄接口
// maasidem.Pool —— 幂等与作用域判定才能在 pgxmock 下做行为级钉死。
// *pgxpool.Pool 天然满足该接口，既有调用方无需改动。
type Service struct {
	pool maasidem.Pool
}

// NewService creates a new inventory service.
func NewService(pool maasidem.Pool) *Service {
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
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.HoldCreateResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "HoldCreate", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldCreateResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldCreateResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
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

	// ★ 超卖防护：在事务内锁定最新快照行，逐行校验
	// available = Σ sellable − (pending + committed holds) ≥ requested。
	// FOR UPDATE 使并发 HoldCreate 串行化，杜绝两个事务同时通过校验。
	// 金额数量用 decimal（与 TEXT 存储的契约类型一致），杜绝浮点误差。
	for _, line := range req.Lines {
		requested, derr := decimal.NewFromString(line.Quantity.Amount)
		if derr != nil {
			return maascontracts.HoldCreateResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorSellableInsufficient,
				Message:   fmt.Sprintf("invalid quantity for %s: %s", line.ModelSKU, line.Quantity.Amount),
				Retryable: false,
			}
		}
		if requested.Sign() <= 0 {
			return maascontracts.HoldCreateResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorSellableInsufficient,
				Message:   fmt.Sprintf("quantity must be positive for %s", line.ModelSKU),
				Retryable: false,
			}
		}

		// 锁定最新非过期快照行（FOR UPDATE 串行化并发预留）。
		var snapshotID string
		err = tx.QueryRow(ctx, `
			SELECT snapshot_id
			FROM maas_inventory_snapshots
			WHERE NOT stale
			ORDER BY observed_at DESC
			LIMIT 1
			FOR UPDATE
		`).Scan(&snapshotID)
		if err == pgx.ErrNoRows {
			return maascontracts.HoldCreateResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorStaleObservation,
				Message:   "no inventory snapshot available",
				Retryable: false,
			}
		}
		if err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("lock inventory snapshot: %w", err)
		}

		var availableAmount string
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(sellable_amount::DECIMAL), 0)::TEXT
			FROM maas_sellable_items
			WHERE snapshot_id = $1 AND model_sku = $2
		`, snapshotID, line.ModelSKU).Scan(&availableAmount)
		if err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("query available inventory: %w", err)
		}

		// 已被 pending + committed holds 占用的数量。
		var heldAmount string
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(hl.quantity_amount::DECIMAL), 0)::TEXT
			FROM maas_hold_lines hl
			JOIN maas_holds h ON hl.hold_id = h.hold_id
			WHERE hl.model_sku = $1
			  AND h.status IN ('pending', 'committed')
			  AND h.expires_at > NOW()
		`, line.ModelSKU).Scan(&heldAmount)
		if err != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("query held inventory: %w", err)
		}

		available, derr := decimal.NewFromString(availableAmount)
		if derr != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("parse available amount: %w", derr)
		}
		held, derr := decimal.NewFromString(heldAmount)
		if derr != nil {
			return maascontracts.HoldCreateResponse{}, fmt.Errorf("parse held amount: %w", derr)
		}

		remaining := available.Sub(held)
		if requested.GreaterThan(remaining) {
			return maascontracts.HoldCreateResponse{}, &maascontracts.Error{
				Code: maascontracts.ErrorSellableInsufficient,
				Message: fmt.Sprintf("insufficient inventory for %s: requested %s, available %s",
					line.ModelSKU, requested.String(), remaining.String()),
				Retryable: true, // 库存可能随后续释放恢复，客户端可重询后重试
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
	// ★ D6：真正序列化 request/response 后落库（旧实现固定写
	// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
	err = maasidem.Record(ctx, tx, tenantID, "HoldCreate", meta.IdempotencyKey, req, resp)
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
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.HoldConfirmResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "HoldConfirm", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldConfirmResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldConfirmResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.HoldConfirmResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
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
	// ★ D6：真正序列化 request/response 后落库（旧实现固定写
	// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
	err = maasidem.Record(ctx, tx, auth.TenantID, "HoldConfirm", meta.IdempotencyKey, req, resp)
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
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.HoldReleaseResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "HoldRelease", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldReleaseResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldReleaseResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.HoldReleaseResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
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
	// ★ D6：真正序列化 request/response 后落库（旧实现固定写
	// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
	err = maasidem.Record(ctx, tx, auth.TenantID, "HoldRelease", meta.IdempotencyKey, req, resp)
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
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.HoldExpireResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "HoldExpire", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.HoldExpireResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.HoldExpireResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.HoldExpireResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
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
		// ★ D6：真正序列化 request/response 后落库（旧实现固定写
		// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
		err = maasidem.Record(ctx, tx, auth.TenantID, "HoldExpire", meta.IdempotencyKey, req, resp)
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

// Verify that Service implements InventoryPort interface at compile time.
var _ maascontracts.InventoryPort = (*Service)(nil)
