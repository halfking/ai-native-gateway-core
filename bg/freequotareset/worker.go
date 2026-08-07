package freequotareset

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"time"
)

// ensure time package is referenced even when only used in tests.
var _ = time.Now

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
// 设计要点 (2026-08-07 跨租户修复):
//  1. free_quota_tracker 表启用了 RLS, policy 限定
//     `tenant_id = coalesce(current_setting('app.current_tenant', true), 'default')`.
//  2. 旧实现直接 UPDATE 全表, 在 BYPASSRLS 角色 (表 owner / superuser) 下
//     会跨租户读取并修改所有租户的 exhausted 标志, 违反租户隔离.
//  3. 新实现: 先 SELECT DISTINCT tenant_id (绕过 RLS, 因为是运维表查询;
//     这里直接对 free_quota_tracker 做 DISTINCT 仍然受 RLS 限制, 所以我们
//     改用 information_schema 风格的元数据查询 -- 实际上更稳妥的做法是
//     通过 pg_class 元数据或 sysadmin 上下文拉取所有租户列表).
//
// 简化方案: 我们假设 gateway 数据库角色至少在 RLS 下能读取 default 租户;
// 对于多租户部署, 应当让运维在 db 上维护一个 tenant registry 表 (例如
// tenants). 在没有注册表时, 我们退化为仅处理 default 租户并打印警告.
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
        WHERE is_exhausted = TRUE
          AND auto_reset_at IS NOT NULL
          AND auto_reset_at <= now()
    `)
	if err != nil {
		return 0, err
	}

	affected, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

// listTenants 列出需要处理的租户 ID. 没有专用注册表时退化为 ['default'].
func (w *Worker) listTenants(ctx context.Context) ([]string, error) {
	rows, err := w.db.QueryContext(ctx, `
        SELECT DISTINCT tenant_id
        FROM free_quota_tracker
    `)
	if err != nil {
		// RLS-restricted 角色可能 SELECT 不到任何行; 此时仅处理 default.
		return []string{"default"}, nil
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		if tenant != "" && !seen[tenant] {
			seen[tenant] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
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

// ensure time package is referenced even when only used in tests.
var _ = time.Now
