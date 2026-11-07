package maas

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// BackfillCreditConsumptionBuckets re-derives the hourly credit-consumption
// counter table from _request_logs.credits_charged over the lookback window
// (typically 90 days, the longest window the dashboard supports).
//
// Idempotency: rows are overwritten (ON CONFLICT … DO UPDATE SET credits =
// EXCLUDED.credits), so repeated runs in the same hour bucket converge to
// the same total that _request_logs reports. ChargeRequest's atomic upsert
// has already been writing the same hour bucket live; the only data that
// could differ between the live counter and the backfill at run-time is
// the request just committed between SELECT and INSERT — which lands in
// the same hour bucket and is reconciled on the next backfill or the
// natural row's eventual re-derivation.
//
// Safe to call from cmd/gateway startup. Designed to be fast (<5s on
// hot 7-day table + monthly partitions). Errors are logged but not
// fatal — dashboard degrades to a zero hint when the counter is empty.
func (s *Service) BackfillCreditConsumptionBuckets(ctx context.Context, lookbackDays int) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	if lookbackDays < 1 {
		lookbackDays = 1
	}
	if lookbackDays > 90 {
		lookbackDays = 90
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// R189（2026-11-07 252 SQL 审计轮）水位短路：桶表已覆盖 lookback 窗时
	// 跳过全量重算。缺陷实测：252 生产（154/245 共库）每次 boot 都重跑
	// 90 天 × _request_logs 分区族的 GROUP BY 聚合，quiet 也要 20-28s；
	// 蓝绿部署日多次 boot ⇒ 大扫描反复白烧，且 30s rolconfig / 60s ctx
	// 双预算经常把尾段杀掉（24h 窗 11 次发射 = 4 完成 + 3 超时 + 4 ctx 取消）。
	// 桶行是 ON CONFLICT 全量覆盖式收敛（本函数幂等），覆盖完整时重算
	// 与跳过等价 ⇒ 水位短路是保守安全 optimization：任何怀疑（查询失败、
	// 空表、两端不齐）都照跑。多实例共库时任一实例覆盖完整即可短路。
	var oldest, newest *time.Time
	wmErr := s.pool.QueryRow(ctx, `
		SELECT MIN(bucket_start), MAX(bucket_start)
		  FROM maas_credit_consumption_buckets
	`).Scan(&oldest, &newest)
	if wmErr == nil && bucketsCoverLookback(time.Now(), oldest, newest, lookbackDays) {
		slog.Info("maas: BackfillCreditConsumptionBuckets skipped — bucket table already covers lookback window",
			"lookback_days", lookbackDays, "oldest_bucket", oldest, "newest_bucket", newest)
		return 0, nil
	}
	// 水位查询失败或两端不齐 ⇒ 照跑全量（fail-open），与既有行为一致。

	logsTable, alias := requestLogsSource(lookbackDays)
	res, err := s.pool.Exec(ctx, `
		INSERT INTO maas_credit_consumption_buckets
			(tenant_id, bucket_start, credits, request_count, updated_at)
		SELECT
			`+alias+`.tenant_id,
			date_trunc('hour', `+alias+`.ts) AS bucket_start,
			COALESCE(SUM(`+RequestLogCreditsSQL(alias, false)+`), 0)::bigint AS credits,
			COUNT(*)::int AS request_count,
			now() AS updated_at
		FROM `+logsTable+`
		WHERE `+alias+`.ts >= now() - ($1::int * INTERVAL '1 day')
		  AND `+alias+`.tenant_id NOT IN ('', 'default')
		  AND (`+alias+`.credits_charged IS NOT NULL
		    OR COALESCE(`+alias+`.prompt_tokens, 0)
		     + COALESCE(`+alias+`.completion_tokens, 0)
		     + COALESCE(`+alias+`.cache_read_tokens, 0)
		     + COALESCE(`+alias+`.cache_write_tokens, 0) > 0)
		GROUP BY `+alias+`.tenant_id, date_trunc('hour', `+alias+`.ts)
		ON CONFLICT (tenant_id, bucket_start) DO UPDATE
			SET credits       = EXCLUDED.credits,
			    request_count = EXCLUDED.request_count,
			    updated_at    = EXCLUDED.updated_at
	`, lookbackDays)
	if err != nil {
		slog.Warn("maas: BackfillCreditConsumptionBuckets failed",
			"lookback_days", lookbackDays, "error", err)
		return 0, fmt.Errorf("backfill credit consumption buckets: %w", err)
	}
	rows := res.RowsAffected()
	slog.Info("maas: BackfillCreditConsumptionBuckets completed",
		"lookback_days", lookbackDays, "rows_affected", rows)
	return rows, nil
}

// bucketsCoverLookback 报告桶表水位是否已覆盖 lookback 窗的两端：
//
//   - 窗头：最老桶不晚于 (now - lookbackDays + 24h)。小时桶的窗头第一桶
//     只要在窗起点后一个自然日内即视为齐 —— 更早的桶本来就在窗外，
//     dashboard 的最长窗（90 天）读不到它们，缺它们不改变任何读数。
//   - 窗尾：最新桶不早于 now - 2h。ChargeRequest 的实时 upsert 本来就在
//     维护当下桶，回填的职责是「历史收敛」，尾部 2h 容差覆盖
//     「实时写已在、回填无需重复」的正常稳态。
//
// oldest/newest 为 nil（空表，MIN/MAX 为 NULL）⇒ 不覆盖 ⇒ 照跑。
func bucketsCoverLookback(now time.Time, oldest, newest *time.Time, lookbackDays int) bool {
	if oldest == nil || newest == nil {
		return false
	}
	windowStart := now.Add(-time.Duration(lookbackDays) * 24 * time.Hour)
	headOK := !oldest.After(windowStart.Add(24 * time.Hour))
	tailOK := !newest.Before(now.Add(-2 * time.Hour))
	return headOK && tailOK
}

// QueryConsumedCreditsWindow sums the hourly bucket counter for a tenant
// over the last `days` days. Returns 0 when the bucket table is missing or
// empty so callers can degrade gracefully.
//
// Used by admin /api/usage/summary as the fast path for
// total_credits_charged (replaces SELECT SUM(credits_charged) FROM
// usage_ledger … which scans all rows in the window).
func (s *Service) QueryConsumedCreditsWindow(ctx context.Context, tenantID string, days int) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	if tenantID == "" {
		return 0, fmt.Errorf("tenant_id required")
	}
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var credits int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`, tenantID, days).Scan(&credits)
	if err != nil {
		return 0, fmt.Errorf("query consumed credits window: %w", err)
	}
	return credits, nil
}
