// Package bg — supplier_error_stats_aggregator.go
//
// 2026-09-05 审计闭环1：supplier_errors_hot → supplier_error_stats 预聚合。
//
// supplier_error_stats 是趋势 API（/api/errors/trend）的唯一读源：
// 前端趋势图不再扫明细表。聚合按时间桶（minute/hour/day）× 维度
// (supplier, credential_id, error_type, model) 分组，UPSERT 到
// V371 定义的唯一键上，重复聚合幂等（审计要求：防重复聚合）。
// 桶内另产出 retryable_count / stage_counts 分桶（2026-09-05 audit E-#6：
// 可重试率与阶段分布的聚合载体），随覆盖式 UPSERT 逐桶刷新。
//
// 窗口语义（2026-09-05 audit D-2#7）：进程内维护 watermark（上次成功
// rollup 的窗口上界）。每次 tick 重算 [watermark, now)，起点 now-10min
// （≈2×interval，与旧固定窗口等价）；成功后推进 watermark，失败不推进
// 自然在下个 tick 重算同一窗口（UPSERT 幂等，重算不重不漏）。窗口上限
// 钳 48h（2026-09-14 审计 #7：base 源改为 hot∪父表 UNION ALL 后，聚合器
// 对已 promote 的行仍可达——columnar 历史只读不改结果，钳窗口只为停摆
// catch-up 的重算量兜底）；停摆超过 48h 的那一段钳掉并告警，趋势 API 对
// 该段走明细兜底。
//
// RLS（2026-09-14 审计 #6）：hot 与父表均 FORCE RLS，聚合在单事务内先
// set_config('app.bypass_rls','true',true) 再跑三条 rollup——顺带把
// minute→hour→day 三级变成原子提交，任一级失败整体回滚、watermark 不动。
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

// supplierErrorStatsMaxWindow 钳制 catch-up 窗口（audit D-2#7；2026-09-14
// 审计 #7 放宽 8h→48h）：base 源改为 hot∪父表 UNION ALL 后，已 promote 进
// columnar 的行对聚合器仍然可达，钳窗口只为停摆 catch-up 的重算量兜底，
// 不再是"数据永久够不着"的硬边界。
const supplierErrorStatsMaxWindow = 48 * time.Hour

// supplierErrorStatsRollupSQL 把 hot 明细按分钟桶聚合 upsert（granularity
// 由参数传入，固定传 "minute"）。错误率分母 total_requests =
// error_count + success_count，其中 success 侧暂无逐行来源（成功请求不写
// 错误表），取 0 —— error_rate 仅在接入成功计数（usage_ledger join）后
// 才有意义，先保持列契约稳定。
//
// 2026-09-05 审计 E-#6：桶内同时产出 retryable_count（is_retryable=true
// 行数）与 stage_counts（stage→计数 jsonb map）。UPSERT 键保持
// (stat_time, granularity, supplier, credential_id, error_type, model)
// 不变：retryable/stage 若提为维度键会把行数乘上组合数、并改变 hour/day
// rollup 的分组语义；分桶计数随覆盖式 UPSERT 逐桶刷新，无重复累计。
// stage_counts 用两层分组（先按 stage 计数再 jsonb_object_agg）是因为
// jsonb_object_agg 不求和；空串 stage 保留为 "" 键（无损），读端映射 unknown。
//
// 2026-09-14 审计 #7：base 源从仅 supplier_errors_hot 改为 hot∪父表
// UNION ALL——promote 是 DELETE+INSERT 原子单语句，任一行只会存在于某一侧，
// UPSERT 覆盖写幂等，无重复计数；聚合器由此覆盖已迁入 columnar 历史的区间。
const supplierErrorStatsRollupSQL = `
WITH base AS (
    SELECT date_bin($1::interval, occurred_at, '2000-01-01'::timestamptz) AS stat_time,
           supplier, credential_id, error_type, model,
           request_id, affected_users, is_retryable, stage
    FROM (
        SELECT occurred_at, supplier, credential_id, error_type, model,
               request_id, affected_users, is_retryable, stage
        FROM supplier_errors_hot
        WHERE occurred_at >= $3 AND occurred_at < $4
        UNION ALL
        SELECT occurred_at, supplier, credential_id, error_type, model,
               request_id, affected_users, is_retryable, stage
        FROM supplier_errors
        WHERE occurred_at >= $3 AND occurred_at < $4
    ) hot_and_historical
), bucket_totals AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           COUNT(*)::int AS error_count,
           COUNT(DISTINCT request_id)::int AS unique_requests,
           SUM(affected_users)::int AS affected_users,
           SUM(CASE WHEN is_retryable THEN 1 ELSE 0 END)::int AS retryable_count
    FROM base
    GROUP BY 1, 2, 3, 4, 5
), bucket_stages AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           jsonb_object_agg(stage, n) AS stage_counts
    FROM (
        SELECT stat_time, supplier, credential_id, error_type, model, stage,
               COUNT(*)::int AS n
        FROM base
        GROUP BY 1, 2, 3, 4, 5, 6
    ) per_stage
    GROUP BY 1, 2, 3, 4, 5
)
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests,
    retryable_count, stage_counts
)
SELECT
    b.stat_time,
    $2,
    b.supplier,
    b.credential_id,
    b.error_type,
    b.model,
    b.error_count,
    b.unique_requests,
    b.affected_users,
    0,
    b.error_count,
    b.retryable_count,
    COALESCE(s.stage_counts, '{}'::jsonb)
FROM bucket_totals b
LEFT JOIN bucket_stages s
       ON b.stat_time = s.stat_time
      AND b.supplier = s.supplier
      AND b.credential_id = s.credential_id
      AND b.error_type = s.error_type
      AND b.model = s.model
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    total_requests  = EXCLUDED.total_requests,
    retryable_count = EXCLUDED.retryable_count,
    stage_counts    = EXCLUDED.stage_counts,
    aggregated_at   = NOW()
`

// supplierErrorStatsHourRollupSQL / supplierErrorStatsDayRollupSQL 做
// minute→hour→day 二次 rollup（audit 2026-09-05 E-#4）：趋势 API 默认
// 24h 窗口查 hour、168h 查 day，只写 minute 会让默认窗口恒走明细兜底。
// 下界用 date_trunc 对齐到桶起点（而非窗口 from），保证每个被重算的
// 桶都聚合了它范围内的**全部**子桶行——UPSERT 覆盖写才不会用部分窗口
// 的残缺和覆盖完整桶。unique_requests 跨桶求和是上界近似（同一请求
// 跨分钟会重复计数），明细级精确去重只有 hot join 一条路，不值得。
// E-#6：retryable_count 直接跨桶求和；stage_counts 经 jsonb_each 展开、
// 按 (桶, stage) 求和后再 jsonb_object_agg 合并（两级聚合绕开
// "聚合函数内不能再聚合"的限制）。
const supplierErrorStatsHourRollupSQL = `
WITH bucket_totals AS (
    SELECT date_trunc('hour', stat_time) AS stat_time,
           supplier, credential_id, error_type, model,
           SUM(error_count)::int AS error_count,
           SUM(unique_requests)::int AS unique_requests,
           SUM(affected_users)::int AS affected_users,
           SUM(success_count)::int AS success_count,
           SUM(total_requests)::int AS total_requests,
           SUM(retryable_count)::int AS retryable_count
    FROM supplier_error_stats
    WHERE granularity = 'minute'
      AND stat_time >= date_trunc('hour', $1::timestamptz)
      AND stat_time < $2
    GROUP BY 1, 2, 3, 4, 5
), bucket_stages AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           jsonb_object_agg(stage, n) AS stage_counts
    FROM (
        SELECT date_trunc('hour', stat_time) AS stat_time,
               supplier, credential_id, error_type, model,
               e.key AS stage, SUM((e.value)::bigint)::int AS n
        FROM supplier_error_stats src
        CROSS JOIN LATERAL jsonb_each(src.stage_counts) e(key, value)
        WHERE src.granularity = 'minute'
          AND src.stat_time >= date_trunc('hour', $1::timestamptz)
          AND src.stat_time < $2
        GROUP BY 1, 2, 3, 4, 5, 6
    ) per_stage
    GROUP BY 1, 2, 3, 4, 5
)
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests,
    retryable_count, stage_counts
)
SELECT
    b.stat_time,
    'hour',
    b.supplier,
    b.credential_id,
    b.error_type,
    b.model,
    b.error_count,
    b.unique_requests,
    b.affected_users,
    b.success_count,
    b.total_requests,
    b.retryable_count,
    COALESCE(s.stage_counts, '{}'::jsonb)
FROM bucket_totals b
LEFT JOIN bucket_stages s
       ON b.stat_time = s.stat_time
      AND b.supplier = s.supplier
      AND b.credential_id = s.credential_id
      AND b.error_type = s.error_type
      AND b.model = s.model
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    success_count   = EXCLUDED.success_count,
    total_requests  = EXCLUDED.total_requests,
    retryable_count = EXCLUDED.retryable_count,
    stage_counts    = EXCLUDED.stage_counts,
    aggregated_at   = NOW()
`

const supplierErrorStatsDayRollupSQL = `
WITH bucket_totals AS (
    SELECT date_trunc('day', stat_time) AS stat_time,
           supplier, credential_id, error_type, model,
           SUM(error_count)::int AS error_count,
           SUM(unique_requests)::int AS unique_requests,
           SUM(affected_users)::int AS affected_users,
           SUM(success_count)::int AS success_count,
           SUM(total_requests)::int AS total_requests,
           SUM(retryable_count)::int AS retryable_count
    FROM supplier_error_stats
    WHERE granularity = 'hour'
      AND stat_time >= date_trunc('day', $1::timestamptz)
      AND stat_time < $2
    GROUP BY 1, 2, 3, 4, 5
), bucket_stages AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           jsonb_object_agg(stage, n) AS stage_counts
    FROM (
        SELECT date_trunc('day', stat_time) AS stat_time,
               supplier, credential_id, error_type, model,
               e.key AS stage, SUM((e.value)::bigint)::int AS n
        FROM supplier_error_stats src
        CROSS JOIN LATERAL jsonb_each(src.stage_counts) e(key, value)
        WHERE src.granularity = 'hour'
          AND src.stat_time >= date_trunc('day', $1::timestamptz)
          AND src.stat_time < $2
        GROUP BY 1, 2, 3, 4, 5, 6
    ) per_stage
    GROUP BY 1, 2, 3, 4, 5
)
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests,
    retryable_count, stage_counts
)
SELECT
    b.stat_time,
    'day',
    b.supplier,
    b.credential_id,
    b.error_type,
    b.model,
    b.error_count,
    b.unique_requests,
    b.affected_users,
    b.success_count,
    b.total_requests,
    b.retryable_count,
    COALESCE(s.stage_counts, '{}'::jsonb)
FROM bucket_totals b
LEFT JOIN bucket_stages s
       ON b.stat_time = s.stat_time
      AND b.supplier = s.supplier
      AND b.credential_id = s.credential_id
      AND b.error_type = s.error_type
      AND b.model = s.model
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    success_count   = EXCLUDED.success_count,
    total_requests  = EXCLUDED.total_requests,
    retryable_count = EXCLUDED.retryable_count,
    stage_counts    = EXCLUDED.stage_counts,
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
//   - watermark 老于 48h：钳到 [now-48h, now) 并返回 clamped=true（catch-up
//     重算量兜底；2026-09-14 审计 #7 后 base 已覆盖 hot∪历史父表，钳制不再
//     代表数据不可达）。
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
// rollup（audit E-#4）。三级在同一条事务内提交（2026-09-14 审计 #6：事务内
// 先设 RLS 旁路，顺带获得三级原子性）；失败整体回滚、watermark 不动，下个
// tick 从同一水位重算——UPSERT 幂等保证重算不重不漏。
func (a *SupplierErrorStatsAggregator) rollup(ctx context.Context) {
	if a == nil || a.db == nil {
		return
	}
	now := time.Now()
	from, to, clamped := a.rollupWindow(now)
	if clamped {
		slog.Warn("supplier_error_stats: watermark older than max window, clamping recompute range "+
			"(catch-up backlog is capped; rows older than the window remain queryable via the unified detail view)",
			"watermark", a.watermark.Format(time.RFC3339),
			"from", from.Format(time.RFC3339),
			"max_window", supplierErrorStatsMaxWindow.String())
	}

	// 单事务包住三级 rollup + RLS 旁路（is_local=true，提交即失效，
	// pooled 连接不保留提权）——任一级失败全部回滚。
	tx, err := a.db.Begin(ctx)
	if err != nil {
		slog.Warn("supplier_error_stats: rollup begin failed", "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		slog.Warn("supplier_error_stats: rollup RLS setup failed", "error", err)
		return
	}

	minuteTag, err := tx.Exec(ctx, supplierErrorStatsRollupSQL,
		a.interval, "minute", from, to)
	if err != nil {
		slog.Warn("supplier_error_stats: minute rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	if n := minuteTag.RowsAffected(); n > 0 {
		slog.Debug("supplier_error_stats: rolled up minute buckets", "buckets", n)
	}
	if _, err := tx.Exec(ctx, supplierErrorStatsHourRollupSQL, from, to); err != nil {
		slog.Warn("supplier_error_stats: hour rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	if _, err := tx.Exec(ctx, supplierErrorStatsDayRollupSQL, from, to); err != nil {
		slog.Warn("supplier_error_stats: day rollup failed",
			"error", err, "from", from, "to", to)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("supplier_error_stats: rollup commit failed", "error", err)
		return
	}
	a.watermark = to
	if n := int64(to.Sub(from)/a.interval) + 1; n > 3 {
		slog.Info("supplier_error_stats: caught up after gap",
			"from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339),
			"intervals", n)
	}
}
