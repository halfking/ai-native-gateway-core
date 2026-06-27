package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withTenantTx wraps a callback in a read-only transaction that sets the
// app.current_tenant GUC. This makes RLS policies on request_logs,
// tenant_tool_policies, tenant_model_policies etc. enforce tenant isolation
// as defense-in-depth on top of the application-level WHERE tenant_id = $N
// filter (added by tenantLogsClause() in admin/session_tenant.go).
//
// Usage:
//
//	err := withTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
//	    rows, err := tx.Query(ctx, "SELECT ... FROM request_logs WHERE tenant_id = $1 AND ...")
//	    // process rows
//	    return err
//	})
//
// The GUC is set via SET LOCAL (transaction-scoped), so it auto-clears on
// commit/rollback. Single quotes in tenantID are escaped to prevent injection.
func withTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	if err := setLocalTenantGUC(ctx, tx, tenantID); err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// withTenantQueryRow (2026-06-27 audit fix) 是 withTenantTx 的 QueryRow 专用版。
//
// 重要：callback 必须**同步**执行 query 并 scan，把结果通过返回值传出。
// 绝对不能返回 pgx.Row 给调用方再 scan——因为事务会在 callback 返回后立刻
// commit，而 pgx.Row 是 lazy 求值（scan 时才真正执行 SQL），这会导致：
//  1. SQL 在已 commit 的事务里执行，破坏事务隔离
//  2. 实际查询逃出 RLS GUC 上下文（SET LOCAL 在 commit 时清掉）
//
// 因此签名是 callback func(tx pgx.Tx) error，调用方在 callback 内完成所有
// query/scan/结果处理；helper 只负责 setTenantGUC + commit/rollback。
func withTenantQueryRow(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	if err := setLocalTenantGUC(ctx, tx, tenantID); err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// setLocalTenantGUC 在事务内设置 SET LOCAL app.current_tenant = '<tenant>'。
// 单引号经过转义防止注入；tenantID 由 caller 控制（admin handler 从 JWT
// 取，不应包含单引号）。
func setLocalTenantGUC(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if tenantID == "" {
		return fmt.Errorf("empty tenant_id for SET LOCAL app.current_tenant")
	}
	escaped := strings.ReplaceAll(tenantID, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_tenant = '"+escaped+"'"); err != nil {
		return fmt.Errorf("set tenant GUC: %w", err)
	}
	return nil
}
