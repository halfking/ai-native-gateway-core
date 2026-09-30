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

func (w *StatsMinuteRollup) retireClosedMain(ctx context.Context, since, until time.Time) error {
	if w == nil || w.db == nil {
		return nil
	}
	if _, err := w.db.Exec(ctx, retireClosedMainMinuteSQL, since, until); err != nil {
		return fmt.Errorf("retire closed minute keys: %w", err)
	}
	return nil
}
