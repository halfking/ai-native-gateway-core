// rls.go — durable 族 RLS GUC 通道（RLS 设计 §五 Phase 1 item 2）。
//
// durable_llm_tasks / durable_task_settlement_intents 的 tenant_isolation
// policy 读 app.current_tenant（`tenant_id = current_setting('app.current_tenant', true)`），
// super_admin_bypass policy 读 app.current_role / app.bypass_rls。在
// llm_gateway 降权（Phase 2）之前这些 GUC 是 dormant 的；补齐后 worker/前台
// 两条路径的 GUC 状态即 Phase 3 durable 批 FORCE 的前置证明。
//
// 规范（与 admin/tenant_ctx.go、apihub/pg_store.go 同形）：
//   - 全部 set_config 第三参为 true（事务级，tx 结束自动回收，无需 RESET——
//     设计 §四 D4）；
//   - 前台写入路径设 app.current_tenant = 租户；
//   - worker 扫描/收割/结算路径设 super_admin + bypass_rls 双分支（与
//     durable_llm_tasks_super_admin_bypass policy 的 USING 形状一一对应）；
//   - worker 族单语句读写必须包显式事务——autocommit 下 is_local GUC 语句
//     结束即回收，等于没设，降权后会被 RLS 过滤成 0 行（假性 ErrLeaseLost/
//     ErrNoRows）。
package durable

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// setAllTenantBypassGUC 在事务内设置 super-admin RLS 旁路双 GUC，供 worker
// 全租户扫描/收割/结算路径使用。语义对齐 admin.setAllTenantGUC。
func setAllTenantBypassGUC(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true)`); err != nil {
		return fmt.Errorf("durable: set super-admin role GUC: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
		return fmt.Errorf("durable: set RLS bypass GUC: %w", err)
	}
	return nil
}

// setLocalTenantGUC 在事务内设置 app.current_tenant，供前台按租户写入路径
// 使用。语义对齐 admin.setLocalTenantGUC。
func setLocalTenantGUC(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if tenantID == "" {
		return fmt.Errorf("durable: empty tenant_id for SET LOCAL app.current_tenant")
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		return fmt.Errorf("durable: set tenant GUC: %w", err)
	}
	return nil
}

// execWithBypassTx 把 worker 族单语句写包进显式事务并设置旁路 GUC。
func (s *Store) execWithBypassTx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, fmt.Errorf("durable: begin bypass tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setAllTenantBypassGUC(ctx, tx); err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pgconn.CommandTag{}, fmt.Errorf("durable: commit bypass tx: %w", err)
	}
	return tag, nil
}

// queryWithBypassTx 把 worker 族单次查询包进显式只读事务并设置旁路 GUC，
// fn 消费查询结果，返回值透传；事务在 fn 返回后立即提交。
func (s *Store) queryWithBypassTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("durable: begin bypass read tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setAllTenantBypassGUC(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("durable: commit bypass read tx: %w", err)
	}
	return nil
}
