// Package admin — analytics_materialized.go
//
// Materialized-view fast path for /api/admin/auto-route/analytics/* and
// /api/admin/auto-route/audit endpoints.
//
// Background (2026-08-31): every aggregation over
// request_logs_with_current_month_without_customer_id Seq-Scans 314K+ rows
// in the current columnar partition and blew past the 15s handler timeout.
// Migration 632 pre-aggregates those rows into routing_analytics_7d
// (hourly buckets × task × model × provider × tenant) and
// routing_audit_summary_7d (per-tenant totals); bg.MaterializedViewRefresher
// keeps them current every 10 minutes.
//
// Fallback contract: when the materialized view is missing, empty, or stale
// (refresher down / disabled), every helper here reports "not usable" and
// callers fall back to the original base-view queries — the endpoints
// degrade to pre-632 behavior instead of serving wrong numbers.
package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mvFreshnessBudget is the maximum staleness we tolerate before falling
// back to the base view. The refresher runs every 10 minutes, so 15 minutes
// gives one missed cycle of headroom; anything older means the refresher is
// down and stale numbers must not be served.
const mvFreshnessBudget = 15 * time.Minute

// mvFreshWithin reports whether the named materialized view exists and was
// refreshed within mvFreshnessBudget. A missing view, a query error, or a
// missing/unknown refresh stamp all report false so callers take the
// base-view fallback.
//
// Migration 837 moved the stamp out of the view and into
// routing_mv_refresh_state. It used to be MAX(refreshed_at) over the view
// itself, where every row carried a NOW() value — which is precisely what
// made each REFRESH rewrite the entire view (a volatile column makes every
// recomputed tuple differ). The refresher now records the time in a
// one-row-per-view table, so the freshness check costs one indexed lookup
// instead of a full aggregate over ~4.4K rows.
//
// Two properties are load-bearing and must not be collapsed into one query:
//   - The view must still EXIST. The stamp survives a DROP (a rebuild drops
//     and recreates the view but leaves the side table alone), so a stamp
//     alone would happily report "fresh" for a view that is gone.
//   - A missing stamp must not be read as fresh. Pre-837 databases, or one
//     where the refresher has never run, have no row yet; that is stale
//     (callers fall back), not fresh.
//
// The view name is bound as a parameter even though every caller passes a
// package-internal constant: an identifier can never be interpolated into
// SQL text by accident this way.
func mvFreshWithin(ctx context.Context, db *pgxpool.Pool, view string) bool {
	return mvFreshWithinBudget(ctx, db, view, mvFreshnessBudget)
}

// sessionMvFreshnessBudget 是 357 会话分析视图族的陈旧度预算（2026-10-07）。
// 它的刷新间隔是 SessionViewsInterval=60min（实测一轮 40-70s，源表 919MB），
// 而 routing 族是 10min —— 沿用 15min 预算会让 session 视图**几乎永远**被判为
// 陈旧（刷新间隔比预算还长 4 倍）。沿用 routing 同一个「预算 > 间隔，留一轮
// 漏刷余量」的原则：60min 间隔 ⇒ 90min 预算。
//
// 为什么需要这道门：357 建完视图时刷新过一次，之后**没有任何刷新排程**
// （245 实测 2026-10-07 06:31:36 填的，16:07 端点仍在返回它，陈旧 9h37m，
// 而 session_summaries 一直在写）。没有门的时候，端点返回的是一份看起来完全
// 正常的数字，没有错误、没有提示 —— 过期被伪装成了「就是这个数」。
//
// ★ 关于「余量」的准确说法（2026-10-08 更正，别再照抄旧措辞）：
// 90min **不是**「间隔之外再留一轮」的余量。按实测值算清楚：
//
//	稳定态最坏年龄 = 60min 间隔 + 64s 一轮刷新 = 61min4s   < 90min  放行
//	漏一轮之后      = 121min4s                             > 90min  503
//
// 也就是说 90min 只容得下**半个周期**的余量，**漏一轮就降级**。
// 这是刻意的选择而不是疏忽：漏一轮后端点会 503 到下一轮成功为止（约一小时，
// 自愈），期间不再把「两小时前的数字」当成结论发出去 —— 而两小时前的数字
// 对一个看板已经没什么参考价值了。
//
// routing 族是 10min 间隔 + ~17s 刷新 vs 15min 预算 ⇒ 漏一轮后 11min < 15min
// 仍放行，**它才是真的留了一轮余量**。两族数值不可互相照搬：
// 照搬「预算 = 间隔 + 一个间隔」会得到 120min，而那会让两小时前的数据照发。
const sessionMvFreshnessBudget = 90 * time.Minute

// mvFreshWithinBudget reports whether the named materialized view exists and
// was refreshed within budget. 缺失视图 / 查询出错 / 无刷新戳一律 false，
// 调用方走降级方向。
func mvFreshWithinBudget(ctx context.Context, db *pgxpool.Pool, view string, budget time.Duration) bool {
	if db == nil {
		return false
	}
	var refreshedAt *time.Time
	var viewExists bool
	if err := db.QueryRow(ctx, `
		SELECT
			(SELECT refreshed_at FROM routing_mv_refresh_state WHERE view_name = $1),
			EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname = 'public' AND matviewname = $1)
	`, view).Scan(&refreshedAt, &viewExists); err != nil {
		// Includes "routing_mv_refresh_state does not exist yet" — a
		// pre-837 database. Falling back is the safe direction: the base
		// queries are slower but correct.
		return false
	}
	if !viewExists || refreshedAt == nil {
		return false
	}
	return time.Since(*refreshedAt) < budget
}

// mvFreshWithinBudgetFor 返回一个绑定了该预算的新鲜度探针，供多视图整体门使用。
func mvFreshWithinBudgetFor(ctx context.Context, db *pgxpool.Pool, budget time.Duration) func(string) bool {
	return func(view string) bool { return mvFreshWithinBudget(ctx, db, view, budget) }
}

// useMaterializedView decides whether analytics endpoints (matrix / flow)
// can serve windowLabel from routing_analytics_7d. Only the 7d window is
// pre-aggregated; 24h always uses the base view for freshness.
func useMaterializedView(ctx context.Context, db *pgxpool.Pool, windowLabel string) bool {
	if windowLabel != "7d" {
		return false
	}
	return mvFreshWithin(ctx, db, "routing_analytics_7d")
}

// buildMatrixQueryMaterialized builds the heatmap query against
// routing_analytics_7d. Semantically it mirrors buildMatrixQuery:
//
//   - row axis  = effective model (outbound_model, falling back to
//     client_model for explicit-model requests); Go-side canonicalization
//     in handleMatrix applies to both paths identically.
//   - col axis  = effective task (__specified__ for explicit-model
//     requests) or work_type.
//   - p95_ms is the one approximation: a request-count-weighted average of
//     hourly bucket p95s instead of an exact percentile over raw rows.
func buildMatrixQueryMaterialized(rowDim, metric string) (string, error) {
	if rowDim != "task_type" && rowDim != "work_type" {
		return "", fmt.Errorf("row must be task_type or work_type")
	}

	var metricExpr string
	switch metric {
	case "count":
		metricExpr = "SUM(request_count)::float8"
	case "success_rate":
		metricExpr = "CASE WHEN SUM(request_count) > 0 THEN SUM(success_count)::float8 / SUM(request_count)::float8 ELSE 0 END"
	case "p95_ms":
		metricExpr = "SUM(p95_latency_ms * request_count) / NULLIF(SUM(request_count), 0)"
	case "cost_usd":
		metricExpr = "COALESCE(SUM(total_cost_usd), 0)"
	default:
		return "", fmt.Errorf("metric must be one of: count, success_rate, p95_ms, cost_usd")
	}

	rowExpr := "effective_model"
	colExpr := "effective_task_type"
	if rowDim == "work_type" {
		colExpr = "effective_work_type"
	}

	return fmt.Sprintf(`
		SELECT %s AS row_key,
		       %s AS col_key,
		       %s AS val
		FROM routing_analytics_7d
		WHERE effective_model IS NOT NULL
		  AND %s IS NOT NULL
		GROUP BY %s, %s
	`, rowExpr, colExpr, metricExpr, colExpr, rowExpr, colExpr), nil
}

// buildFlowL12QueryMaterialized builds the L1→L2 (task → model) Sankey
// query against routing_analytics_7d. Mirrors buildFlowL12Query.
func buildFlowL12QueryMaterialized() string {
	return `
		SELECT effective_task_type AS src,
		       effective_model AS dst,
		       SUM(request_count)::float8 AS val
		FROM routing_analytics_7d
		WHERE effective_task_type IS NOT NULL
		  AND effective_model IS NOT NULL
		GROUP BY effective_task_type, effective_model
	`
}

// buildFlowL23QueryMaterialized builds the L2→L3 (model × task → provider)
// Sankey query. The base query (buildFlowL23Query) resolves a provider via
// `COALESCE(provider_id, credential lookup)`; migration 632 bakes the same
// fallback into the view's effective_provider_id column so both paths agree
// on how much traffic lands in the 'unknown' provider bucket.
func buildFlowL23QueryMaterialized() string {
	return `
		SELECT mv.effective_task_type AS task_type,
		       mv.effective_model AS src,
		       COALESCE(p.display_name, 'unknown') AS dst,
		       SUM(mv.request_count)::float8 AS val
		FROM routing_analytics_7d mv
		LEFT JOIN providers p ON p.id = mv.effective_provider_id
		WHERE mv.effective_task_type IS NOT NULL
		  AND mv.effective_model IS NOT NULL
		GROUP BY mv.effective_task_type, mv.effective_model, p.display_name
	`
}

// getAuditSummaryMaterialized retrieves the headline 7-day counters for
// handleAudit from routing_audit_summary_7d. tenantID follows the string
// form returned by tenantLogsClause (admin/session_tenant.go); nil means
// super-admin, which sums every tenant group — the same rows the base
// query counts. The freshness gate keeps a dead refresher from freezing
// the audit numbers forever.
//
// The final return value reports whether the materialized path produced
// usable numbers; on false the caller must run the base-view query.
func getAuditSummaryMaterialized(ctx context.Context, db *pgxpool.Pool, tenantID *string) (total, successes, totalAuto, totalSpecified int64, ok bool) {
	if db == nil || !mvFreshWithin(ctx, db, "routing_audit_summary_7d") {
		return 0, 0, 0, 0, false
	}

	var err error
	if tenantID != nil {
		err = db.QueryRow(ctx, `
			SELECT total_requests, success_count, auto_request_count, specified_request_count
			FROM routing_audit_summary_7d
			WHERE tenant_id = $1
		`, *tenantID).Scan(&total, &successes, &totalAuto, &totalSpecified)
	} else {
		err = db.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(total_requests), 0),
				COALESCE(SUM(success_count), 0),
				COALESCE(SUM(auto_request_count), 0),
				COALESCE(SUM(specified_request_count), 0)
			FROM routing_audit_summary_7d
		`).Scan(&total, &successes, &totalAuto, &totalSpecified)
	}
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return total, successes, totalAuto, totalSpecified, true
}
