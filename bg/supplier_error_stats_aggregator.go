// Package bg — supplier_error_stats_aggregator.go
//
// 2026-09-05 审计闭环1：supplier_errors_hot → supplier_error_stats 预聚合。
//
// supplier_error_stats 是趋势 API（/api/errors/trend）的唯一读源：
// 前端趋势图不再扫明细表。聚合按时间桶（minute/hour/day）× 维度
// (supplier, credential_id, error_type, model) 分组，UPSERT 到
// V371 定义的唯一键上，重复聚合幂等（审计要求：防重复聚合）。
//
// 与 ProviderErrorAggregator 的差异：无 watermark。stats 的时间桶 +
// ON CONFLICT UPDATE 语义天然幂等，重跑同一窗口只刷新同一行；
// 同时窗口末尾的 minute 桶仍在增长（写入有延迟），所以每次 tick
// 重算「最近两个窗口」的桶，覆盖迟到行。
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const supplierErrorStatsDefaultInterval = 5 * time.Minute

// supplierErrorStatsRollupSQL 把 hot 明细按分钟桶聚合 upsert。
// 错误率分母 total_requests = error_count + success_count，其中 success
// 侧暂无逐行来源（成功请求不写错误表），取 0 —— error_rate 仅在接入
// 成功计数（usage_ledger join）后才有意义，先保持列契约稳定。
const supplierErrorStatsRollupSQL = `
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests
)
SELECT
    date_bin($1::interval, occurred_at, '2000-01-01'::timestamptz),
    $2,
    supplier,
    credential_id,
    error_type,
    model,
    COUNT(*)::int,
    COUNT(DISTINCT request_id)::int,
    SUM(affected_users)::int,
    0,
    COUNT(*)::int
FROM supplier_errors_hot
WHERE occurred_at >= $3 AND occurred_at < $4
GROUP BY 1, 2, 3, 4, 5, 6
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    total_requests  = EXCLUDED.total_requests,
    aggregated_at   = NOW()
`

// SupplierErrorStatsAggregator 周期性把 supplier_errors_hot 聚合到
// supplier_error_stats（minute 桶）。nil pool 安全（lite 模式无 PG）。
type SupplierErrorStatsAggregator struct {
	db       *pgxpool.Pool
	interval time.Duration
	*BaseWorker
}

func NewSupplierErrorStatsAggregator(db *pgxpool.Pool, interval time.Duration) *SupplierErrorStatsAggregator {
	if interval <= 0 {
		interval = supplierErrorStatsDefaultInterval
	}
	return &SupplierErrorStatsAggregator{
		db:         db,
		interval:   interval,
		BaseWorker: NewBaseWorker("supplier-error-stats-aggregator"),
	}
}

// Start 启动聚合循环；重复调用不会启动多个 worker。
func (a *SupplierErrorStatsAggregator) Start(ctx context.Context) {
	if a == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.BaseWorker.Start(ctx, a.run)
}

// Stop 停止后台任务；重复调用及未 Start 都安全返回。
func (a *SupplierErrorStatsAggregator) Stop() {
	if a == nil {
		return
	}
	a.BaseWorker.Stop()
}

func (a *SupplierErrorStatsAggregator) run(ctx context.Context) {
	defer a.BaseWorker.NotifyStopped()
	a.rollup(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.rollup(ctx)
		}
	}
}

// rollup 重算最近两个 interval 的 minute 桶：覆盖迟到写入，
// UPSERT 保证幂等（审计要求「唯一约束防重复聚合」）。
func (a *SupplierErrorStatsAggregator) rollup(ctx context.Context) {
	if a == nil || a.db == nil {
		return
	}
	now := time.Now()
	from := now.Add(-2 * a.interval)
	to := now
	tag, err := a.db.Exec(ctx, supplierErrorStatsRollupSQL,
		a.interval, "minute", from, to)
	if err != nil {
		slog.Warn("supplier_error_stats: rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Debug("supplier_error_stats: rolled up", "buckets", n)
	}
}
