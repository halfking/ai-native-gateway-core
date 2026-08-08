package freequotareset

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"time"
)

const superAdminRole = "super_admin"

// Worker 配额重置后台任务
type Worker struct {
	db       *sql.DB
	interval time.Duration
}

// NewWorker 创建配额重置 Worker
func NewWorker(db *sql.DB, interval time.Duration) *Worker {
	if interval == 0 {
		interval = 5 * time.Minute // 默认 5 分钟
	}
	return &Worker{
		db:       db,
		interval: interval,
	}
}

// Run 启动后台任务（阻塞）
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	log.Printf("[FreeQuotaReset] Worker started (interval: %v)", w.interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[FreeQuotaReset] Worker stopped")
			return
		case <-ticker.C:
			if err := w.resetExpiredWindows(ctx); err != nil {
				log.Printf("[FreeQuotaReset] Error: %v", err)
			}
		}
	}
}

// resetExpiredWindows 重置已过期的耗尽状态。
//
// 设计要点 (2026-08-07 跨租户修复 + 2026-08-08 H1 闭合):
//  1. free_quota_tracker 表启用了 RLS, policy 限定
//     `tenant_id = coalesce(current_setting('app.current_tenant', true), 'default')`.
//  2. 旧实现直接 UPDATE 全表, 在 BYPASSRLS 角色 (表 owner / superuser) 下
//     会跨租户读取并修改所有租户的 exhausted 标志, 违反租户隔离.
//  3. 新实现: 事务内 SET LOCAL app.current_role = 'super_admin' 走 RLS
//     policy 显式放行的 super_admin 通道枚举所有 tenant, 然后对每个
//     tenant 开新事务 SET LOCAL app.current_tenant = '<tenant>' 让
//     RLS 实际生效. super_admin 是 policy 已经显式白名单的角色, 不依赖
//     BYPASSRLS 隐式放行, 审计链路可追溯.
func (w *Worker) resetExpiredWindows(ctx context.Context) error {
	tenants, err := w.listTenants(ctx)
	if err != nil {
		return err
	}

	totalRows := int64(0)
	for _, tenant := range tenants {
		affected, err := w.resetTenant(ctx, tenant)
		if err != nil {
			log.Printf("[FreeQuotaReset] tenant=%s error: %v", tenant, err)
			continue
		}
		totalRows += affected
	}

	if totalRows > 0 {
		log.Printf("[FreeQuotaReset] Reset %d expired quota windows across %d tenant(s)", totalRows, len(tenants))
	}
	return nil
}

// resetTenant 在事务内设置 app.current_tenant 后执行 UPDATE, 让 RLS 实际生效.
func (w *Worker) resetTenant(ctx context.Context, tenantID string) (int64, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.current_tenant', $1, $2)", tenantID, true); err != nil {
		return 0, err
	}

	result, err := tx.ExecContext(ctx, `
        UPDATE free_quota_tracker
        SET is_exhausted = FALSE,
            exhausted_at = NULL
		WHERE tenant_id = $1
		  AND is_exhausted = TRUE
		  AND auto_reset_at IS NOT NULL
		  AND auto_reset_at <= now()
	`, tenantID)
	if err != nil {
		return 0, err
	}

	affected, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

// listTenants 枚举 free_quota_tracker 中实际存在的租户 ID. 在事务内
// SET LOCAL app.current_role = 'super_admin' (is_local=true 让 GUC 只
// 作用于当前事务), 借 RLS policy 显式白名单的 super_admin 通道跨租户
// 枚举. 这样既保留多租户支持, 又不污染连接级 GUC, 也不依赖 BYPASSRLS
// 的隐式放行. 退化: RLS 拒绝/表为空 → ['default'] (与旧 fallback 一致).
func (w *Worker) listTenants(ctx context.Context) ([]string, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return []string{"default"}, nil
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.current_role', $1, true)",
		superAdminRole, true); err != nil {
		return []string{"default"}, nil
	}

	rows, err := tx.QueryContext(ctx, `
        SELECT DISTINCT tenant_id
        FROM free_quota_tracker
    `)
	if err != nil {
		// RLS-restricted 角色可能 SELECT 不到任何行; 此时仅处理 default.
		return []string{"default"}, nil
	}

	seen := map[string]bool{}
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			rows.Close()
			return nil, err
		}
		if tenant != "" && !seen[tenant] {
			seen[tenant] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if err := tx.Commit(); err == nil {
		committed = true
	}

	if len(seen) == 0 {
		return []string{"default"}, nil
	}

	out := make([]string, 0, len(seen))
	for tenant := range seen {
		out = append(out, tenant)
	}
	sort.Strings(out)
	return out, nil
}
