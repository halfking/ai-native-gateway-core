package admin

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// isPlatformCreditsScope reports whether total_credits_charged should reflect
// the whole platform. Both "" (super_admin all-tenants) and "default" (platform
// tenant) map to platform-wide consumption.
func isPlatformCreditsScope(tenantID string) bool {
	return tenantID == "" || tenantID == "default"
}

// queryTotalCreditsCharged reads the dashboard "总积分消耗" KPI.
//
// Platform scope (default tenant / super_admin): sum all tenants, including
// estimated credits for default-tenant traffic that is not wallet-billed.
//
// Other tenants: only that tenant's own consumption.
//
// IMPORTANT: this must never be inlined into the main usageSummary SELECT.
//
// ## 为什么返回 (值, 降级视图名) 两个结果
//
// 2026-10-03 之前这里只返回 int64，42P01 时静默 `return 0`。
// 调用方有两条路径：
//
//	· admin/usage.go:150 —— summary 主查询**成功**之后才调它。
//	  那条路径的 degraded 标记只覆盖主查询失败；主查询成功而 credits 降级时，
//	  载荷会是 `total_credits_charged: 0` + `degraded: false`。
//	· admin/dashboard_board_queries.go:113 —— 看板首屏 KPI。
//
// 积分在 BoardHeroRow.vue 是**高亮卡片**（第 21 行直接 fmt 出来），
// 用户看到「总积分消耗 0」而没有任何「这个数不可信」的提示。
//
// 这与本轮已修的 5 处裸数组降级同型：降级本身站得住（真的没数据源），
// 但页面把它当成真值。修法是让它把降级自报出来，由调用方决定怎么标。
func (h *Handler) queryTotalCreditsCharged(ctx context.Context, tenantID string, days int) (int64, string) {
	if h.db == nil {
		return 0, ""
	}
	platform := isPlatformCreditsScope(tenantID)
	bucketTenant := tenantID
	if platform {
		bucketTenant = ""
	}

	credits, bucketRows, err := h.queryCreditsFromBuckets(ctx, bucketTenant, days)
	if err == nil && bucketRows > 0 {
		if !platform {
			return credits, ""
		}
		// Billed tenants are in buckets; default traffic is estimated separately.
		defaultCredits, estErr := h.queryCreditsFromRequestLogs(ctx, "default", days, false)
		if estErr != nil {
			slog.Warn("usageSummary: default-tenant credits estimate failed",
				"days", days, "error", estErr)
			// 账单积分可信、估算部分不可信 → 报部分降级。
			// 静默返回 credits 会让「总积分」少算一截而无提示。
			return credits, creditDegradedView(estErr)
		}
		return credits + defaultCredits, ""
	}
	if err != nil && !IsMissingRelationError(err) {
		slog.Warn("usageSummary: credits bucket query failed, falling back to request_logs",
			"tenant_id", tenantID,
			"days", days,
			"error", err)
	}
	logsCredits, logsErr := h.queryCreditsFromRequestLogs(ctx, tenantID, days, platform)
	if logsErr != nil {
		if IsMissingRelationError(logsErr) {
			slog.Debug("usageSummary: credits request_logs fallback unavailable",
				"relation", ExtractMissingRelationName(logsErr))
			// 两条腿都断了 → 0 是「没算出来」，不是「消耗为 0」。
			return 0, creditDegradedView(logsErr)
		}
		slog.Warn("usageSummary: credits request_logs fallback failed",
			"tenant_id", tenantID,
			"days", days,
			"error", logsErr)
		return 0, ""
	}
	return logsCredits, ""
}

// creditDegradedView 返回可供载荷自报的缺失视图名；非 42P01 返回空串
// （那类错误走 slog.Warn，不进 degraded 契约——契约针对的是「可迁移性缺失」）。
func creditDegradedView(err error) string {
	if !IsMissingRelationError(err) {
		return ""
	}
	return ExtractMissingRelationName(err)
}

func (h *Handler) queryCreditsFromBuckets(ctx context.Context, tenantID string, days int) (credits int64, bucketRows int64, err error) {
	if tenantID != "" {
		err = h.db.QueryRow(ctx, `
			SELECT COALESCE(SUM(credits), 0)::bigint,
			       COUNT(*)::bigint
			  FROM maas_credit_consumption_buckets
			 WHERE tenant_id = $1
			   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
		`, tenantID, days).Scan(&credits, &bucketRows)
		return credits, bucketRows, err
	}
	err = h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint,
		       COUNT(*)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE bucket_start >= now() - ($1::int * INTERVAL '1 day')
	`, days).Scan(&credits, &bucketRows)
	return credits, bucketRows, err
}

// requestLogsFromClause mirrors maas.requestLogsSource for admin usage queries.
func requestLogsFromClause(days int) (from string, alias string) {
	if days <= 7 {
		return "request_logs_hot AS r", "r"
	}
	return "request_logs_with_current_month AS r", "r"
}

func requestLogsBillableClause(alias string) string {
	return `(` + alias + `.credits_charged IS NOT NULL
		  OR COALESCE(` + alias + `.prompt_tokens, 0)
		   + COALESCE(` + alias + `.completion_tokens, 0)
		   + COALESCE(` + alias + `.cache_read_tokens, 0)
		   + COALESCE(` + alias + `.cache_write_tokens, 0) > 0)`
}

func (h *Handler) queryCreditsFromRequestLogs(ctx context.Context, tenantID string, days int, platformScope bool) (int64, error) {
	logsTable, alias := requestLogsFromClause(days)
	includeDefault := platformScope || tenantID == "default"
	whereClause := alias + `.ts >= now() - ($1 * INTERVAL '1 day')`
	args := []any{days}
	if platformScope {
		whereClause += " AND " + alias + `.tenant_id <> ''`
	} else {
		whereClause += " AND " + alias + `.tenant_id = $2`
		args = append(args, tenantID)
	}
	whereClause += " AND " + requestLogsBillableClause(alias)
	var credits int64
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(`+maas.RequestLogCreditsSQL(alias, includeDefault)+`), 0)::bigint
		  FROM `+logsTable+`
		 WHERE `+whereClause, args...).Scan(&credits)
	return credits, err
}
