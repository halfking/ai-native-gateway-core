package trading

import (
	"context"
	"fmt"
	"github.com/kaixuan/llm-gateway-go/internal/maasidem"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Service implements the EntitlementPort interface for customer entitlement management.
// Entitlements represent customer rights to consume resources after successful trading.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new trading service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// generateEntitlementID creates a unique entitlement identifier with ent_ prefix.
func generateEntitlementID() string {
	return fmt.Sprintf("ent_%d_%s", time.Now().UnixNano(), randomHex(8))
}

// randomHex generates a random hex string of the specified length.
func randomHex(n int) string {
	return fmt.Sprintf("%x", time.Now().UnixNano())[:n]
}

// ActivateEntitlement activates customer entitlement after committed reservation.
// Preconditions per OpenAPI v0.1:
//   - Referenced hold must be in "committed" status
//   - Legal signoff reference should be present (business rules apply)
func (s *Service) ActivateEntitlement(
	ctx context.Context,
	auth maascontracts.AuthContext,
	meta maascontracts.CommandMeta,
	req maascontracts.ActivateEntitlementRequest,
) (maascontracts.ActivateEntitlementResponse, error) {
	// Check idempotency first
	// ★ D8（2026-11-19 审计轮）：幂等键必填。
	// 契约（contracts/maascontracts/types.go 的 CommandMeta）明写
	// "Required for all commands"，此前实现零校验 ⇒ 同一 tenant +
	// command_type 下所有空键命令互相误判为重放。
	if err := maasidem.RequireKey(meta.IdempotencyKey); err != nil {
		return maascontracts.ActivateEntitlementResponse{}, err
	}
	cachedResp, err := maasidem.Check(ctx, s.pool, auth.TenantID, "ActivateEntitlement", meta.IdempotencyKey)
	if err != nil {
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("check idempotency: %w", err)
	}
	if cachedResp != nil {
		var resp maascontracts.ActivateEntitlementResponse
		// ★ D6：真正反序列化缓存响应 —— 重放必须拿到与首次执行一致的
		// 响应（旧实现返回零值空结构，客户端无法对账）。
		if err := maasidem.Replay(cachedResp, &resp); err != nil {
			return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("replay idempotent response: %w", err)
		}
		return resp, nil
	}

	// Get tenant ID from request or auth context
	tenantID := auth.TenantID
	if req.TenantID != nil && *req.TenantID != "" {
		// Verify tenant ID matches auth context
		if *req.TenantID != auth.TenantID {
			return maascontracts.ActivateEntitlementResponse{}, &maascontracts.Error{
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
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Verify hold exists and is in committed status
	var holdStatus string
	var holdTenantID string
	err = tx.QueryRow(ctx, `
		SELECT status, tenant_id
		FROM maas_holds
		WHERE hold_id = $1
		FOR UPDATE
	`, req.HoldID).Scan(&holdStatus, &holdTenantID)

	if err == pgx.ErrNoRows {
		return maascontracts.ActivateEntitlementResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorReserveNotCommitted,
			Message:   fmt.Sprintf("hold %s not found", req.HoldID),
			Retryable: false,
		}
	}
	if err != nil {
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("query hold: %w", err)
	}

	// Verify tenant matches
	if holdTenantID != tenantID {
		return maascontracts.ActivateEntitlementResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorUnauthorizedScope,
			Message:   "hold belongs to a different tenant",
			Retryable: false,
		}
	}

	// Verify hold is in committed status
	if holdStatus != string(maascontracts.HoldStatusCommitted) {
		return maascontracts.ActivateEntitlementResponse{}, &maascontracts.Error{
			Code:      maascontracts.ErrorReserveNotCommitted,
			Message:   fmt.Sprintf("hold status is %s, expected committed", holdStatus),
			Retryable: false,
		}
	}

	// Check if entitlement already exists for this hold (idempotency at data level)
	var existingEntitlementID string
	err = tx.QueryRow(ctx, `
		SELECT entitlement_id
		FROM maas_entitlements
		WHERE hold_id = $1 AND tenant_id = $2
		LIMIT 1
	`, req.HoldID, tenantID).Scan(&existingEntitlementID)

	if err == nil {
		// Entitlement already exists, return it (idempotent)
		var status string
		var activatedAt *time.Time
		err = tx.QueryRow(ctx, `
			SELECT status, activated_at
			FROM maas_entitlements
			WHERE entitlement_id = $1
		`, existingEntitlementID).Scan(&status, &activatedAt)

		if err != nil {
			return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("query existing entitlement: %w", err)
		}

		return maascontracts.ActivateEntitlementResponse{
			EntitlementID: existingEntitlementID,
			Status:        maascontracts.EntitlementStatus(status),
			HoldID:        req.HoldID,
			ActivatedAt:   activatedAt,
		}, nil
	} else if err != pgx.ErrNoRows {
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("check existing entitlement: %w", err)
	}

	// Validate legal signoff reference if business rules require it
	// For now, we accept it as optional
	// TODO: Add business rule validation based on hold value/type

	// Generate entitlement ID
	entitlementID := generateEntitlementID()
	now := time.Now().UTC()

	// Insert entitlement record
	_, err = tx.Exec(ctx, `
		INSERT INTO maas_entitlements (
			entitlement_id, tenant_id, hold_id, status,
			legal_signoff_ref, contract_id, activated_at,
			idempotency_key, trace_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
	`,
		entitlementID,
		tenantID,
		req.HoldID,
		string(maascontracts.EntitlementStatusActive),
		req.LegalSignoffRef,
		req.ContractID,
		now,
		meta.IdempotencyKey,
		auth.TraceID,
	)
	if err != nil {
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("insert entitlement: %w", err)
	}

	resp := maascontracts.ActivateEntitlementResponse{
		EntitlementID: entitlementID,
		Status:        maascontracts.EntitlementStatusActive,
		HoldID:        req.HoldID,
		ActivatedAt:   &now,
	}

	// Record idempotency log
	// ★ D6：真正序列化 request/response 后落库（旧实现固定写
	// '{}'::jsonb ⇒ payload 从未落库，重放只能解出空结构）。
	err = maasidem.Record(ctx, tx, tenantID, "ActivateEntitlement", meta.IdempotencyKey, req, resp)
	if err != nil {
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("record idempotency: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return maascontracts.ActivateEntitlementResponse{}, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

// Verify that Service implements EntitlementPort interface at compile time.
var _ maascontracts.EntitlementPort = (*Service)(nil)
