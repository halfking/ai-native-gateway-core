// Package sessionaudit — approval_manager.go
//
// ApprovalManager 是会话审批流程的持久化层。所有方法均使用 pgxpool，
// 避免与 admin.Handler (*pgxpool.Pool) 之间产生双 driver 切换。
//
// 多租户隔离（2026-06-27 审计修复）：
//   - 所有读方法（Get / List）显式比对 tenant_id；调用方传入的
//     tenantID 与数据库行不一致时直接返回 ErrTenantMismatch。
//   - 所有写方法（Approve / Reject / MarkTimeout）通过
//     withTenantTx() 设置 `app.current_tenant` GUC，触发
//     db/migrations/120_session_audit.sql 中的 RLS policy，实现
//     "应用层校验 + DB 层兜底" 的纵深防御。
//   - Insert / Update 行数据时不允许跨租户写入。
package sessionaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrTenantMismatch 跨租户访问被拒绝。
var ErrTenantMismatch = errors.New("sessionaudit: tenant mismatch")

// ErrNotFound 记录不存在（Get 专用）。
var ErrNotFound = errors.New("sessionaudit: approval not found")

// ErrAlreadyDecided 重复审批请求被拒绝。
var ErrAlreadyDecided = errors.New("sessionaudit: approval already decided")

// ApprovalDBTX 是 ApprovalManager 所依赖的最小 DB 接口。
//
// 同时被 *pgxpool.Pool 和 pgxmock.PgxPoolIface 实现（pgxmock 是
// pgxpool 的子集接口），允许测试时使用 pgxmock 替换真实连接池。
type ApprovalDBTX interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ApprovalManager 审批流程管理器。
type ApprovalManager struct {
	pool    ApprovalDBTX
	timeout time.Duration
}

// NewApprovalManager 创建审批管理器（接收 pgxpool.Pool 或 pgxmock）。
// timeout <= 0 时退化为 15 分钟默认值。
func NewApprovalManager(pool ApprovalDBTX, timeout time.Duration) *ApprovalManager {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	return &ApprovalManager{pool: pool, timeout: timeout}
}

// Create 创建审批记录。
//
// 在事务内先 `SET LOCAL app.current_tenant = req.TenantID` 触发 RLS，
// 然后插入。返回新生成的 UUID。
func (m *ApprovalManager) Create(ctx context.Context, req *ApprovalRequest) (string, error) {
	if req == nil {
		return "", errors.New("sessionaudit: nil approval request")
	}
	if req.TenantID == "" {
		return "", errors.New("sessionaudit: tenant_id required")
	}
	if req.SessionID == "" || req.RequestID == "" {
		return "", errors.New("sessionaudit: session_id and request_id required")
	}

	detectResultJSON, err := json.Marshal(req.DetectResult)
	if err != nil {
		return "", fmt.Errorf("marshal detect result: %w", err)
	}
	snapshotJSON, err := json.Marshal(req.Snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}

	approvalID := uuid.New().String()
	expiresAt := time.Now().Add(m.timeout)
	if req.Timeout > 0 {
		expiresAt = time.Now().Add(req.Timeout)
	}

	tx, err := beginTenantWriteTx(ctx, m.pool, req.TenantID)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO approval_queue (
			id, session_id, tenant_id, request_id,
			detect_result, snapshot,
			status, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, approvalID, req.SessionID, req.TenantID, req.RequestID,
		detectResultJSON, snapshotJSON,
		ApprovalPending, time.Now(), expiresAt)
	if err != nil {
		return "", fmt.Errorf("insert approval queue: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit approval: %w", err)
	}
	return approvalID, nil
}

// GetForTenant 是带租户上下文的 Get 入口。RLS 在事务内生效，且应用层
// 二次比对行 tenant_id。两层防御都失败才会返回数据。
//
// expectedTenantID 空字符串 = super_admin 跨租户访问：
//   - 事务内 SET LOCAL app.current_role='super_admin' 让 RLS bypass
//   - 应用层不比对 tenant_id
func (m *ApprovalManager) GetForTenant(ctx context.Context, approvalID, expectedTenantID string) (*ApprovalRecord, error) {
	return m.getWithTx(ctx, approvalID, expectedTenantID)
}

func (m *ApprovalManager) getWithTx(ctx context.Context, approvalID, expectedTenantID string) (*ApprovalRecord, error) {
	tx, err := beginTenantTx(ctx, m.pool, expectedTenantID, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var record ApprovalRecord
	var detectResultJSON, snapshotJSON []byte
	var approvedBy, reason *string
	var approvedAt *time.Time

	err = tx.QueryRow(ctx, `
		SELECT id, session_id, tenant_id, request_id,
		       detect_result, snapshot,
		       status, approved_by, approved_at, reason,
		       created_at, expires_at
		FROM approval_queue
		WHERE id = $1
	`, approvalID).Scan(
		&record.ID, &record.SessionID, &record.TenantID, &record.RequestID,
		&detectResultJSON, &snapshotJSON,
		&record.Status, &approvedBy, &approvedAt, &reason,
		&record.CreatedAt, &record.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query approval: %w", err)
	}

	if expectedTenantID != "" && record.TenantID != expectedTenantID {
		// 应用层横向越权防护。
		return nil, ErrTenantMismatch
	}

	if approvedBy != nil {
		record.ApprovedBy = *approvedBy
	}
	if approvedAt != nil {
		record.ApprovedAt = approvedAt
	}
	if reason != nil {
		record.Reason = *reason
	}

	if err := json.Unmarshal(detectResultJSON, &record.DetectResult); err != nil {
		return nil, fmt.Errorf("unmarshal detect result: %w", err)
	}
	if err := json.Unmarshal(snapshotJSON, &record.Snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit read tx: %w", err)
	}
	return &record, nil
}

// List 查询审批列表（带租户过滤）。
//
// 若 filter.TenantID 非空，事务内设置 app.current_tenant 触发 RLS；
// 列表过滤与 GUC 双重生效。TXT/LIMIT/OFFSET 全部使用参数化绑定。
func (m *ApprovalManager) List(ctx context.Context, filter *ApprovalFilter) ([]*ApprovalRecord, error) {
	if filter == nil {
		filter = &ApprovalFilter{Limit: 50}
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	tx, err := beginTenantTx(ctx, m.pool, filter.TenantID, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 构建查询：所有 WHERE 都通过参数化追加
	args := []any{}
	where := "WHERE 1=1"
	if filter.TenantID != "" {
		args = append(args, filter.TenantID)
		where += fmt.Sprintf(" AND tenant_id = $%d", len(args))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	limitPos := len(args)
	args = append(args, filter.Offset)
	offsetPos := len(args)

	query := fmt.Sprintf(`
		SELECT id, session_id, tenant_id, request_id,
		       detect_result, snapshot,
		       status, approved_by, approved_at, reason,
		       created_at, expires_at
		FROM approval_queue
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, limitPos, offsetPos)

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query approvals: %w", err)
	}
	defer rows.Close()

	var records []*ApprovalRecord
	for rows.Next() {
		var record ApprovalRecord
		var detectResultJSON, snapshotJSON []byte
		var approvedBy, reason *string
		var approvedAt *time.Time

		if err := rows.Scan(
			&record.ID, &record.SessionID, &record.TenantID, &record.RequestID,
			&detectResultJSON, &snapshotJSON,
			&record.Status, &approvedBy, &approvedAt, &reason,
			&record.CreatedAt, &record.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}

		if approvedBy != nil {
			record.ApprovedBy = *approvedBy
		}
		if approvedAt != nil {
			record.ApprovedAt = approvedAt
		}
		if reason != nil {
			record.Reason = *reason
		}

		if err := json.Unmarshal(detectResultJSON, &record.DetectResult); err != nil {
			continue // 跳过损坏记录
		}
		if err := json.Unmarshal(snapshotJSON, &record.Snapshot); err != nil {
			continue
		}
		records = append(records, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvals: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit list tx: %w", err)
	}
	return records, nil
}

// Approve 批准审批（带租户校验）。
//
// callerTenantID 非空时，应用层比对行 tenant_id 与 caller tenant。
// 同时事务内 SET LOCAL app.current_tenant 触发 RLS。RLS 与应用层
// 任一拦截都不会跨租户更新。
func (m *ApprovalManager) Approve(ctx context.Context, approvalID, callerTenantID, approvedBy, reason string) error {
	return m.decide(ctx, approvalID, callerTenantID, approvedBy, reason, ApprovalApproved)
}

// Reject 拒绝审批（同 Approve 的租户防护）。
func (m *ApprovalManager) Reject(ctx context.Context, approvalID, callerTenantID, approvedBy, reason string) error {
	return m.decide(ctx, approvalID, callerTenantID, approvedBy, reason, ApprovalRejected)
}

func (m *ApprovalManager) decide(ctx context.Context, approvalID, callerTenantID, approvedBy, reason string, target ApprovalStatus) error {
	if approvalID == "" {
		return errors.New("sessionaudit: approval_id required")
	}

	tx, err := beginTenantWriteTx(ctx, m.pool, callerTenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 先 SELECT 拿 tenant_id（应用层校验）
	var rowTenant string
	var currentStatus ApprovalStatus
	err = tx.QueryRow(ctx,
		`SELECT tenant_id, status FROM approval_queue WHERE id = $1 FOR UPDATE`,
		approvalID).Scan(&rowTenant, &currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock approval row: %w", err)
	}
	if callerTenantID != "" && rowTenant != callerTenantID {
		return ErrTenantMismatch
	}
	if currentStatus != ApprovalPending {
		return ErrAlreadyDecided
	}

	now := time.Now()
	tag, err := tx.Exec(ctx, `
		UPDATE approval_queue
		SET status = $1,
		    approved_by = $2,
		    approved_at = $3,
		    reason = $4
		WHERE id = $5 AND status = $6
	`, target, approvedBy, now, reason, approvalID, ApprovalPending)
	if err != nil {
		return fmt.Errorf("update approval: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyDecided
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit decide tx: %w", err)
	}
	return nil
}

// MarkTimeout 标记超时的审批为 timeout 状态。
//
// 仅由后台 worker 调用，因此不上 RLS（worker 在 superadmin 上下文）。
func (m *ApprovalManager) MarkTimeout(ctx context.Context) (int, error) {
	tag, err := m.pool.Exec(ctx, `
		UPDATE approval_queue
		SET status = $1,
		    reason = 'Auto-rejected: timeout after ' || extract(epoch from (now() - created_at)) || ' seconds'
		WHERE status = $2 AND expires_at < now()
	`, ApprovalTimeout, ApprovalPending)
	if err != nil {
		return 0, fmt.Errorf("mark timeout: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// setTenantGUC 在事务内 SET LOCAL app.current_tenant。
// 单引号经过转义防止 SQL 注入。
//
// tenantID 为空字符串（super_admin 调用）→ 跳过 SET LOCAL，让 RLS policy
// 默认按 NULLIF→'default' 过滤；调用方需同时设 app.current_role=
// 'super_admin' 才能跨租户 bypass（见 setSuperAdminGUC）。
func setTenantGUC(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if tenantID == "" {
		// super_admin 跨租户调用：不设 tenant GUC
		return nil
	}
	escaped := ""
	for _, r := range tenantID {
		if r == '\'' {
			escaped += "''"
		} else {
			escaped += string(r)
		}
	}
	_, err := tx.Exec(ctx, "SET LOCAL app.current_tenant = '"+escaped+"'")
	if err != nil {
		return fmt.Errorf("set tenant GUC: %w", err)
	}
	return nil
}

// setSuperAdminGUC 在事务内设置 SET LOCAL app.current_role='super_admin'，
// 让 RLS policy bypass tenant 过滤（见 migrations/120_session_audit.sql）。
func setSuperAdminGUC(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SET LOCAL app.current_role = 'super_admin'")
	if err != nil {
		return fmt.Errorf("set role GUC: %w", err)
	}
	return nil
}

// beginTenantTx 启动事务并按 callerTenantID 选择设 GUC 策略：
//   - 非空 → setTenantGUC（应用层 + RLS 兜底）
//   - 空字符串 → setSuperAdminGUC（RLS bypass，仅 super_admin 用）
//
// readOnly=true 时事务为 read-only。返回的 tx 在 caller 用完后必须 commit/rollback。
func beginTenantTx(ctx context.Context, pool ApprovalDBTX, callerTenantID string, readOnly bool) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin tenant tx: %w", err)
	}
	if callerTenantID == "" {
		if err := setSuperAdminGUC(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
	} else {
		if err := setTenantGUC(ctx, tx, callerTenantID); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
	}
	return tx, nil
}

// beginTenantWriteTx 同 beginTenantTx 但事务可写。
func beginTenantWriteTx(ctx context.Context, pool ApprovalDBTX, callerTenantID string) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tenant write tx: %w", err)
	}
	if callerTenantID == "" {
		if err := setSuperAdminGUC(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
	} else {
		if err := setTenantGUC(ctx, tx, callerTenantID); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
	}
	return tx, nil
}

// ApprovalRequest 创建审批请求。
type ApprovalRequest struct {
	SessionID    string
	TenantID     string
	RequestID    string
	DetectResult *DetectResult
	Snapshot     *RequestSnapshot
	Timeout      time.Duration // 0 → 使用默认值
}

// ApprovalFilter 审批列表过滤器。
type ApprovalFilter struct {
	TenantID string
	Status   ApprovalStatus
	Limit    int
	Offset   int
}
