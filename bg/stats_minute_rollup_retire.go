package bg

import (
	"context"
	"fmt"
	"time"
)

// retireClosedMainMinuteSQL 删除扫描窗里已经结束的分钟中，视图不再产出的键。
//
// 上界是 date_trunc(until) 的开区间。当前这一分钟仍由 minute_flush 按
// request_logs.canonical_id 累加；视图在已有 session_turns 时用 turn 的
// canonical（经常仍是 NULL）和另一套 ts。删掉当前分钟会把尚未被 turn
// 覆盖的累加行清掉。
const retireClosedMainMinuteSQL = `
DELETE FROM request_stats_minute AS m
WHERE m.bucket >= date_trunc('minute', $1::timestamptz)
  AND m.bucket < date_trunc('minute', $2::timestamptz)
  AND NOT EXISTS (
    SELECT 1
    FROM request_logs_with_current_month AS r
    WHERE r.request_status IN ('success', 'failure', 'rate_limited')
      AND r.ts >= $1
      AND r.ts < date_trunc('minute', $2::timestamptz)
      AND date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' = m.bucket
      AND COALESCE(NULLIF(r.tenant_id, ''), 'default') = m.tenant_id
      AND COALESCE(r.provider_id, 0) = m.provider_id
      AND COALESCE(r.canonical_id, 0) = m.canonical_id
  )
`

// retireGraceWindow 是闭分钟键退役的回看宽限窗：retire 的扫描下界会被
// 拉到 min(游标 since, until-grace)，即每个 tick 都重扫最近 grace 内的
// 已闭分钟。
//
// 为什么需要：分钟累加器的 flush ticker（30s）可能晚于 rollup ticker
// （60s）落库——请求在分钟 M 内完成、累加行却在 M+1:00~M+1:30 才 upsert
// （增量加语义，会**重建**被 retire 刚删掉的悬空键），而下一轮 rollup 的
// since=M+1:00 永远不再覆盖 M，悬空键就此永生、英雄卡恢复双计（12h 审计
// 实测时序）。重扫窗让重建的悬空键最多存活到下一个 rollup tick 即被再退役。
// 视图仍产出的键由 NOT EXISTS 保护，重扫不会误删（子查询的 r.ts 下界用的
// 是同一个 $1，随外层下界一起放宽）。
const retireGraceWindow = 2 * time.Minute

func (w *StatsMinuteRollup) retireClosedMain(ctx context.Context, since, until time.Time) error {
	if w == nil || w.db == nil {
		return nil
	}
	if _, err := w.db.Exec(ctx, retireClosedMainMinuteSQL, retireScanFloor(since, until), until); err != nil {
		return fmt.Errorf("retire closed minute keys: %w", err)
	}
	return nil
}

// retireScanFloor 计算 retire 的扫描下界：min(since, until-grace)。
// 游标落后于 grace 窗（如首轮回填）时取 since，行为与历史一致；
// 游标已追平（常态）时取 until-grace，实现迟到冲刷重扫。
func retireScanFloor(since, until time.Time) time.Time {
	floor := until.Add(-retireGraceWindow)
	if since.Before(floor) {
		return since
	}
	return floor
}
