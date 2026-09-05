// Package bg — supplier_error_stats_aggregator.go
//
// 2026-09-05 审计闭环1：supplier_errors_hot → supplier_error_stats 预聚合。
//
// supplier_error_stats 是趋势 API（/api/errors/trend）的唯一读源：
// 前端趋势图不再扫明细表。聚合按时间桶（minute/hour/day）× 维度
// (supplier, credential_id, error_type, model) 分组，UPSERT 到
// V371 定义的唯一键上，重复聚合幂等（审计要求：防重复聚合）。
//
// 窗口语义（2026-09-05 audit D-2#7）：进程内维护 watermark（上次成功
// rollup 的窗口上界）。每次 tick 重算 [watermark, now)，起点 now-10min
// （≈2×interval，与旧固定窗口等价）；成功后推进 watermark，失败不推进
// 自然在下个 tick 重算同一窗口（UPSERT 幂等，重算不重不漏）。窗口上限
// 钳 8h：停摆超过 8h 的那一段，hot 行可能已被 promote 搬进 columnar
// （聚合器永久够不着），继续重算更老区间只是浪费——趋势 API 对该段
// 走明细兜底。
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const supplierErrorStatsDefaultInterval = 5 * time.Minute

// supplierErrorStatsInitialLookback 是首个 tick（尚无 watermark）的回看
// 窗口：重算最近 10min（≈2×默认 interval），覆盖迟到写入——与修复前的
// 固定 [now-10min, now) 窗口等价。
const supplierErrorStatsInitialLookback = 10 * time.Minute

// supplierErrorStatsMaxWindow 钳制 catch-up 窗口（audit D-2#7）：聚合器
// 停摆 >8h 后 hot 行可能已被 promote 迁走（8h hot 保留），更老的区间
// 重算只会得到残缺结果，钳到 8h 并告警。
const supplierErrorStatsMaxWindow = 8 * time.Hour

// supplierErrorStatsRollupSQL 把 hot 明细按分钟桶聚合 upsert（granularity
// 由参数传入，固定传 "minute"）。错误率分母 total_requests =
// error_count + success_count，其中 success 侧暂无逐行来源（成功请求不写
// 错误表），取 0 —— error_rate 仅在接入成功计数（usage_ledger join）后
// 才有意义，先保持列契约稳定。
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

// supplierErrorStatsHourRollupSQL / supplierErrorStatsDayRollupSQL 做
// minute→hour→day 二次 rollup（audit 2026-09-05 E-#4）：趋势 API 默认
// 24h 窗口查 hour、168h 查 day，只写 minute 会让默认窗口恒走明细兜底。
// 下界用 date_trunc 对齐到桶起点（而非窗口 from），保证每个被重算的
// 桶都聚合了它范围内的**全部**子桶行——UPSERT 覆盖写才不会用部分窗口
// 的残缺和覆盖完整桶。unique_requests 跨桶求和是上界近似（同一请求
// 跨分钟会重复计数），明细级精确去重只有 hot join 一条路，不值得。
const supplierErrorStatsHourRollupSQL = `
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests
)
SELECT
    date_trunc('hour', stat_time),
    'hour',
    supplier,
    credential_id,
    error_type,
    model,
    SUM(error_count)::int,
    SUM(unique_requests)::int,
    SUM(affected_users)::int,
    SUM(success_count)::int,
    SUM(total_requests)::int
FROM supplier_error_stats
WHERE granularity = 'minute'
  AND stat_time >= date_trunc('hour', $1::timestamptz)
  AND stat_time < $2
GROUP BY 1, 3, 4, 5, 6
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    success_count   = EXCLUDED.success_count,
    total_requests  = EXCLUDED.total_requests,
    aggregated_at   = NOW()
`

const supplierErrorStatsDayRollupSQL = `
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests
)
SELECT
    date_trunc('day', stat_time),
    'day',
    supplier,
    credential_id,
    error_type,
    model,
    SUM(error_count)::int,
    SUM(unique_requests)::int,
    SUM(affected_users)::int,
    SUM(success_count)::int,
    SUM(total_requests)::int
FROM supplier_error_stats
WHERE granularity = 'hour'
  AND stat_time >= date_trunc('day', $1::timestamptz)
  AND stat_time < $2
GROUP BY 1, 3, 4, 5, 6
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    success_count   = EXCLUDED.success_count,
    total_requests  = EXCLUDED.total_requests,
    aggregated_at   = NOW()
`

// SupplierErrorStatsAggregator 周期性把 supplier_errors_hot 聚合到
// supplier_error_stats（minute 桶 + hour/day 二次 rollup）。nil pool 安全
// （lite 模式无 PG）。watermark 只在 run 协程内读写，无需加锁。
type SupplierErrorStatsAggregator struct {
	db        *pgxpool.Pool
	interval  time.Duration
	watermark time.Time // 上次成功 rollup 的窗口上界；零值 = 尚未成功过
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

// rollupWindow 返回本次要重算的 [from, to) 窗口（audit D-2#7）：
//   - 无 watermark（首 tick / 从未成功过）：[now-10min, now)，与修复前的
//     固定「最近两个 interval」窗口等价；
//   - 有 watermark：[watermark, now)——失败过的 tick 不推进 watermark，
//     自然在下个 tick 重算同一窗口（迟到行/停摆期数据被补聚）；
//   - watermark 老于 8h：钳到 [now-8h, now) 并返回 clamped=true（更老的
//     hot 行可能已被 promote 迁走，重算只能得到残缺结果）。
func (a *SupplierErrorStatsAggregator) rollupWindow(now time.Time) (from, to time.Time, clamped bool) {
	to = now
	if a.watermark.IsZero() {
		return now.Add(-supplierErrorStatsInitialLookback), to, false
	}
	if earliest := to.Add(-supplierErrorStatsMaxWindow); a.watermark.Before(earliest) {
		return earliest, to, true
	}
	return a.watermark, to, false
}

// rollup 重算 [watermark, now) 的 minute 桶，并做 minute→hour→day 二次
// rollup（audit E-#4）。三级全部成功才推进 watermark；任一级失败提前
// 返回、watermark 不动，下个 tick 从同一水位重算——UPSERT 幂等保证
// 重算不重不漏。
func (a *SupplierErrorStatsAggregator) rollup(ctx context.Context) {
	if a == nil || a.db == nil {
		return
	}
	now := time.Now()
	from, to, clamped := a.rollupWindow(now)
	if clamped {
		slog.Warn("supplier_error_stats: watermark older than max window, clamping recompute range "+
			"(rows older than the 8h hot window may already be promoted and permanently out of reach)",
			"watermark", a.watermark.Format(time.RFC3339),
			"from", from.Format(time.RFC3339),
			"max_window", supplierErrorStatsMaxWindow.String())
	}

	minuteTag, err := a.db.Exec(ctx, supplierErrorStatsRollupSQL,
		a.interval, "minute", from, to)
	if err != nil {
		slog.Warn("supplier_error_stats: minute rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	if n := minuteTag.RowsAffected(); n > 0 {
		slog.Debug("supplier_error_stats: rolled up minute buckets", "buckets", n)
	}
	if _, err := a.db.Exec(ctx, supplierErrorStatsHourRollupSQL, from, to); err != nil {
		slog.Warn("supplier_error_stats: hour rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	if _, err := a.db.Exec(ctx, supplierErrorStatsDayRollupSQL, from, to); err != nil {
		slog.Warn("supplier_error_stats: day rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	a.watermark = to
	if n := int64(to.Sub(from)/a.interval) + 1; n > 3 {
		slog.Info("supplier_error_stats: caught up after gap",
			"from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339),
			"intervals", n)
	}
}
