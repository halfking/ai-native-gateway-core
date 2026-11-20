package ledger

import (
	"context"
	"fmt"
	"github.com/kaixuan/llm-gateway-go/internal/maasidem"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Service implements the LedgerPort interface for usage tracking and settlement.
// The ledger maintains an append-only immutable audit trail of usage and corrections.
//
// ★ D7 / D9（2026-11-19 审计轮）：pool 由具体类型放宽为窄接口
// maasidem.Pool —— 跨租户作用域判定（UsageCorrect 是否带 tenant_id
// 过滤、拒绝路径是否真的不写库）才能在 pgxmock 下做**行为级**钉死，
// 见 d9_tenant_scope_test.go。*pgxpool.Pool 天然满足该接口，
// 既有调用方无需改动。
type Service struct {
	pool maasidem.Pool
}

// NewService creates a new ledger service.
func NewService(pool maasidem.Pool) *Service {
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

	// ★ 配额门控：请求级预占必须先通过 maas_check_quota_available 校验，
	// 并在事务内原子增加 held_amount。无配额记录或余额不足时拒绝 ——
	// 无白嫖（串行门闩 L 的核心保证）。
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

		// 原子增加 held 配额（quota_version 递增留审计痕迹）。
		_, err = tx.Exec(ctx, `
			UPDATE maas_entitlement_quotas
			SET held_amount = (held_amount::DECIMAL + $1::DECIMAL)::TEXT,
			    quota_version = quota_version + 1,
			    updated_at = NOW()
			WHERE entitlement_id = $2 AND model_sku = $3
		`, req.Quantity.Amount, req.EntitlementID, *req.ModelSKU)
		if err != nil {
			return maascontracts.UsageHoldResponse{}, fmt.Errorf("increment held quota: %w", err)
		}
	}

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
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.UsageSettleResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "UsageSettle", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.UsageSettleResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.UsageSettleResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.UsageSettleResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
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

	// ★ 配额结算：held → consumed 转移，保持账平恒等式
	// allocated = consumed + held + available。
	// 原子扣减 held_amount 并累加 consumed_amount（quota_version 递增审计）。
	if modelSKU != nil && *modelSKU != "" {
		_, err = tx.Exec(ctx, `
			UPDATE maas_entitlement_quotas
			SET held_amount = GREATEST(held_amount::DECIMAL - $1::DECIMAL, 0)::TEXT,
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
	// ★ D6：真正序列化 request/response 后落库（旧实现固定写
	// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
	err = maasidem.Record(ctx, tx, tenantID, "UsageSettle", meta.IdempotencyKey, req, resp)
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
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.UsageCorrectResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "UsageCorrect", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.UsageCorrectResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.UsageCorrectResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
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

	// Verify entry exists **and belongs to this tenant**
	// (we don't lock it, as corrections are append-only)
	//
	// ★ D9（2026-11-19 审计轮）：原查询是
	//   `SELECT EXISTS(SELECT 1 FROM maas_usage_settlements WHERE entry_id = $1)`
	// ——**无 tenant_id 过滤** ⇒ A 租户可以对 B 租户的 entry_id 落一条
	// maas_usage_corrections 冲正行（跨租户冲正）。
	// 对比同文件 UsageSettle 有 holdTenantID != tenantID 校验 ⇒ 同层内
	// 自相矛盾。
	var entryExists bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM maas_usage_settlements st
			JOIN maas_usage_holds h ON st.usage_hold_id = h.usage_hold_id
			WHERE st.entry_id = $1 AND h.tenant_id = $2
		)
	`, req.EntryID, tenantID).Scan(&entryExists)

	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("check entry exists: %w", err)
	}

	if !entryExists {
		// ★ D9：区分「不存在」与「存在但不属于本租户」——后者是越权尝试，
		// 不能回显「该 entry 存在」（那本身就是跨租户存在性泄露）。
		var entryOwnedByOther bool
		err = tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM maas_usage_settlements WHERE entry_id = $1
			)
		`, req.EntryID).Scan(&entryOwnedByOther)
		if err != nil {
			return maascontracts.UsageCorrectResponse{}, fmt.Errorf("check entry ownership: %w", err)
		}
		if entryOwnedByOther {
			return maascontracts.UsageCorrectResponse{}, &maascontracts.Error{
				Code:      maascontracts.ErrorUnauthorizedScope,
				Message:   "ledger entry does not belong to the authenticated tenant",
				Retryable: false,
			}
		}
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
	// ★ D6：真正序列化 request/response 后落库（旧实现固定写
	// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
	err = maasidem.Record(ctx, tx, tenantID, "UsageCorrect", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.UsageCorrectResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// Verify that Service implements LedgerPort interface at compile time.
var _ maascontracts.LedgerPort = (*Service)(nil)
