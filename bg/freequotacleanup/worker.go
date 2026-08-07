package freequotacleanup

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"time"
)

// Worker 配额历史清理后台任务
type Worker struct {
	db       *sql.DB
	interval time.Duration
}

// NewWorker 创建配额清理 Worker
func NewWorker(db *sql.DB, interval time.Duration) *Worker {
	if interval == 0 {
		interval = 24 * time.Hour // 默认每天执行一次
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

	log.Printf("[FreeQuotaCleanup] Worker started (interval: %v)", w.interval)

	// 启动时立即执行一次清理
	if err := w.cleanupOldWindows(ctx); err != nil {
		log.Printf("[FreeQuotaCleanup] Initial cleanup error: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[FreeQuotaCleanup] Worker stopped")
			return
		case <-ticker.C:
			if err := w.cleanupOldWindows(ctx); err != nil {
				log.Printf("[FreeQuotaCleanup] Error: %v", err)
			}
		}
	}
}

// cleanupOldWindows 清理过期的配额追踪记录.
//
// 跨租户修复 (2026-08-07):
//
//	旧实现直接 DELETE FROM free_quota_tracker WHERE window_end < ..., 在
//	BYPASSRLS 角色下会跨租户删除, 违反 RLS 隔离语义. 新实现按 tenant_id
//	分组循环, 每个事务内 SET LOCAL app.current_tenant = '<tenant>', 让
//	RLS policy 实际生效.
func (w *Worker) cleanupOldWindows(ctx context.Context) error {
	tenants, err := w.listTenants(ctx)
	if err != nil {
		return err
	}

	totalRows := int64(0)
	for _, tenant := range tenants {
		affected, err := w.cleanupTenant(ctx, tenant)
		if err != nil {
			log.Printf("[FreeQuotaCleanup] tenant=%s error: %v", tenant, err)
			continue
		}
		totalRows += affected
	}

	if totalRows > 0 {
		log.Printf("[FreeQuotaCleanup] Cleaned up %d old quota windows across %d tenant(s)", totalRows, len(tenants))
	}
	return nil
}

// cleanupTenant 在事务内设置 app.current_tenant 后执行 DELETE.
func (w *Worker) cleanupTenant(ctx context.Context, tenantID string) (int64, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.current_tenant', $1, $2)", tenantID, true); err != nil {
		return 0, err
	}

	result, err := tx.ExecContext(ctx, `
		DELETE FROM free_quota_tracker
		WHERE tenant_id = $1
		  AND ((window_type = 'hour-5' AND window_end < now() - interval '48 hours')
		    OR (window_type = 'day-1' AND window_end < now() - interval '30 days')
		    OR (window_type = 'day-7' AND window_end < now() - interval '90 days')
		    OR (window_type = 'month-1' AND window_end < now() - interval '12 months'))
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

// listTenants 列出需要处理的租户 ID. 没有专用注册表时退化为 ['default'].
func (w *Worker) listTenants(ctx context.Context) ([]string, error) {
	rows, err := w.db.QueryContext(ctx, `
        SELECT DISTINCT tenant_id
        FROM free_quota_tracker
    `)
	if err != nil {
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
