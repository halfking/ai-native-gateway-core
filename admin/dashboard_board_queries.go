package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func (h *Handler) shouldUseBoardLogsFallback(ctx context.Context, tenantID string, tr boardTimeRange) bool {
	if tr.Days <= 1 {
		return false
	}
	where, args := boardMinuteWhere(tr, tenantID, 0)
	var minBucket sql.NullTime
	err := h.db.QueryRow(ctx, `
		SELECT MIN(bucket) FROM request_stats_minute WHERE `+where, args...).Scan(&minBucket)
	if err != nil || !minBucket.Valid {
		return true
	}
	return minBucket.Time.After(tr.Start.Add(boardStatsCoverageSlack(tr)))
}

func trendPointsCoverRange(tr boardTimeRange, points []boardTrendPoint) bool {
	if len(points) == 0 {
		return false
	}
	earliest, err := time.Parse(time.RFC3339, points[0].Bucket)
	if err != nil {
		return false
	}
	return !earliest.After(tr.Start.Add(boardStatsCoverageSlack(tr)))
}

func (h *Handler) queryBoardSummary(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, bool) {
	where, args := boardMinuteWhere(tr, tenantID, 0)

	var totalReq, successCnt, failCnt, promptTok, compTok, totalTok, credits, latencySum int64
	var costUSD float64
	err := h.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(success_count), 0),
			COALESCE(SUM(failure_count), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(latency_ms_sum), 0)
		FROM request_stats_minute
		WHERE `+where+`
	`, args...).Scan(
		&totalReq, &successCnt, &failCnt, &promptTok, &compTok, &totalTok, &credits, &costUSD, &latencySum,
	)
	if err != nil || totalReq == 0 {
		return nil, false
	}
	// 2026-08-31: when minute data has rows, use it as the authoritative
	// source. The previous version forced the fallback for days>1 even
	// when minute data was present (correctly covering recent days),
	// which made every days>1 board request run a 1s fallbackBoardSummary
	// aggregation on top of an already-fast minute pass. Keep the
	// fallback for the empty case only.

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(successCnt) / float64(totalReq)
	}
	avgLatency := 0.0
	if totalReq > 0 {
		avgLatency = float64(latencySum) / float64(totalReq)
	}

	return map[string]any{
		"total_requests":          totalReq,
		"total_prompt_tokens":     promptTok,
		"total_completion_tokens": compTok,
		"total_tokens":            totalTok,
		"total_cost_usd":          costUSD,
		"total_credits_charged":   credits,
		"success_rate":            successRate,
		"avg_latency_ms":          avgLatency,
		"active_api_keys":         activeKeys,
		"active_models":           activeModels,
		"providers":               providers,
	}, true
}

// fallbackBoardSummary 在 minute 统计不可用时走 request_logs 聚合。
//
// ## 2026-10-03：原来这里 `_ = ...Scan(...)` 丢弃错误
//
// 后果不是「少了一个降级点」，而是**整屏数字都是 0 且完全无标记**：
// 请求数 0、Token 0、费用 $0.00。page 上看起来就是「这个时段没有流量」。
// 同文件的 fallbackBoardPies / fallbackBoardTrends / fallbackErrorDrill
// 都返回 error，只有这一个把错误吃掉了。
//
// 现在返回 (payload, err)；调用方据此打降级标记。
func (h *Handler) fallbackBoardSummary(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, error) {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)

	var totalReq int64
	var promptTok, compTok sql.NullInt64
	var costUSD sql.NullFloat64
	var avgLatency sql.NullFloat64
	var successRate sql.NullFloat64
	scanErr := h.db.QueryRow(ctx, `
		SELECT COUNT(*),
			COALESCE(SUM(`+alias+`.prompt_tokens), 0),
			COALESCE(SUM(`+alias+`.completion_tokens), 0),
			COALESCE(SUM(`+alias+`.cost_usd), 0),
			COALESCE(AVG(`+alias+`.latency_ms) FILTER (WHERE `+alias+`.latency_ms IS NOT NULL), 0),
			COALESCE(AVG(CASE WHEN `+alias+`.success THEN 1.0 ELSE 0.0 END), 0)
		FROM `+logsTable+`
		WHERE `+where+` AND `+alias+`.request_status IN ('success', 'failure', 'rate_limited')
	`, args...).Scan(&totalReq, &promptTok, &compTok, &costUSD, &avgLatency, &successRate)

	// 主聚合查询失败 → 整屏数字都不可信。**仍然返回载荷**（保持既有
	// 「降级也返回 200」的形状），但必须带 degraded 让调用方/前端能区分。
	// 只有 42P01 归入本契约：那是「可迁移性缺失」；
	// 其它错误照旧上抛，由上层变成 500 —— 把超时说成「0 请求」是更坏的谎。
	if scanErr != nil && !IsMissingRelationError(scanErr) {
		return nil, scanErr
	}

	// 2026-10-03 的原始动机（上游注释，保留）：回退路径的积分与主路径的
	// request_stats_minute.credits_charged 曾是两个数据源，缺源时返回 0 却无标记。
	//
	// 本次改成 queryBoardCreditsExcludingProbes 而非 queryTotalCreditsCharged：
	//   · 同源 —— 主路径的 request_stats_minute.credits_charged 本身就是
	//     maas.RequestLogCreditsSQL 对 request_logs 算出来的，回退路径读同源，
	//     1d/7d/30d 切页时数字才不会因为换源而跳变（上游注释指出的正是这个缺陷）；
	//   · 同口径 —— 其余读面（请求数/成功率/Token/费用）都经 boardLogsWhere 排除了
	//     探针，积分若仍取自计费账本（账本无探针标记、探针轮次是真的被扣费的），
	//     同一屏就会出现「Token 已排除探针、积分含探针」的自相矛盾。
	//
	// 降级契约不变：queryBoardCreditsExcludingProbes 与 queryTotalCreditsCharged
	// 同签名 (int64, string)，42P01 仍以 credits_missing_view 上报，前端仍显示「—」。
	credits, creditsDegradedView := h.queryBoardCreditsExcludingProbes(ctx, tenantID, tr)

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	payload := map[string]any{
		"total_requests":          totalReq,
		"total_prompt_tokens":     promptTok.Int64,
		"total_completion_tokens": compTok.Int64,
		"total_tokens":            promptTok.Int64 + compTok.Int64,
		"total_cost_usd":          costUSD.Float64,
		"total_credits_charged":   credits,
		"success_rate":            successRate.Float64,
		"avg_latency_ms":          avgLatency.Float64,
		"active_api_keys":         activeKeys,
		"active_models":           activeModels,
		"providers":               providers,
	}
	if scanErr != nil {
		// 42P01：主聚合查询缺表/缺视图，上面所有数字都是 0。
		// 这个载荷仍然是 200（保持既有降级形状），但必须自报家门。
		view := ExtractMissingRelationName(scanErr)
		ReportMissingRelation(slog.Default(), "boardSummaryFallback", scanErr)
		payload["degraded_summary"] = true
		payload["summary_missing_view"] = view
		payload["summary_hint"] = fmt.Sprintf(
			"数据源 %s 不可用，本页所有汇总数字（请求/Token/费用）均为 0，不可作为结论", view)
		// ⚠ 不在这里写 payload["degraded"] / payload["degraded_reason"]：
		// 那两个键在**载荷顶层**属于 board 整体（pies/trends，见
		// applyBoardDegradation），而这里的 summary 是其中一个子对象。
		// 2026-10-03 之前的写法让两种作用域共用一对键，于是
		//   · credits 降级会覆盖掉 summary 的整屏降级原因（后者先写）；
		//   · 前端 BoardHeroRow 的 creditsDegraded 读 summary.degraded，
		//     而整屏降级也会把它置 true ⇒ 积分卡显示「不可信」，
		//     但整屏明明有真实数字可显示。
		// 作用域不同的标记必须有不同的键，判据才有意义。
	}
	if creditsDegradedView != "" {
		payload["degraded"] = true
		payload["degraded_reason"] = "credits: " + creditsDegradedView
		payload["credits_missing_view"] = creditsDegradedView
		payload["credits_hint"] = missingRelationHint(creditsDegradedView)
	}
	return payload, nil
}

// queryBoardCreditsExcludingProbes computes the board's 总积分消耗 without
// falling back to the shared queryTotalCreditsCharged.
//
// Why the board cannot use queryTotalCreditsCharged: that helper reads
// maas_credit_consumption_buckets, the billing ledger. The ledger stores
// charged credits only — it carries no origin_stage / origin_actor /
// quality_flags / task_type, so probe traffic cannot be filtered out of it
// after the fact. Probe rounds really do get charged: the self-check key is
// created with tenant_id='default' (bg/self_check_worker.go:901) and
// shouldChargeUsage (domains/streaming/billing.go:9) inspects only token counts
// and failure stage, never the probe markers.
//
// So the board derives credits from request_logs with the same exclusion every
// other board read face uses. That also makes this path AGREE with the minute
// rollup, which computes credits the same way (maas.RequestLogCreditsSQL over
// probe-filtered rows) — before this change the two paths answered the same
// field by different, non-comparable means.
//
// Scope (deliberate): billing stays untouched. queryTotalCreditsCharged is
// shared with the usage page (admin/usage.go:150) and still reports actual
// charged credits including probes; only the dashboard's operational view is
// probe-free. If probes should not be billable at all, that is a separate
// decision in shouldChargeUsage, not a statistics fix.
//
// Second return value mirrors queryTotalCreditsCharged's degraded-view
// contract (admin/usage_credits.go:creditDegradedView): 42P01 means "not
// computed", and the caller turns it into credits_missing_view so the board
// renders "—" instead of a fabricated 0. Returning a bare 0 here would
// reintroduce exactly the lie the upstream fix removed.
func (h *Handler) queryBoardCreditsExcludingProbes(ctx context.Context, tenantID string, tr boardTimeRange) (int64, string) {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)
	where += " AND " + alias + `.request_status IN ('success', 'failure', 'rate_limited')`
	where += " AND " + requestLogsBillableClause(alias)

	creditsExpr := maas.RequestLogCreditsSQL(alias, tenantID == "" || tenantID == "default")
	var credits int64
	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(`+creditsExpr+`), 0)::bigint
		FROM `+logsTable+`
		WHERE `+where, args...).Scan(&credits); err != nil {
		// 42P01 → 「没算出来」，上报缺失视图让前端显示「—」；
		// 其它错误按 queryTotalCreditsCharged 的同款口径走 slog.Warn，
		// 不进 degraded 契约（契约只针对可迁移性缺失）。
		if IsMissingRelationError(err) {
			return 0, creditDegradedView(err)
		}
		slog.Warn("board credits (probe-excluded) failed",
			"tenant_id", tenantID,
			"start", tr.Start,
			"end", tr.End,
			"error", err)
		return 0, ""
	}
	return credits, ""
}

func (h *Handler) queryOverviewCounts(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	if err := h.queryOverviewCountsMinute(ctx, tenantID, tr, keys, models, providers); err != nil {
		if !IsMissingRelationError(err) {
			return err
		}
	}
	return h.fillOverviewCountsFromLogs(ctx, tenantID, tr, keys, models, providers)
}

func (h *Handler) queryOverviewCountsMinute(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	if tenantID != "" {
		return h.db.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM api_keys WHERE tenant_id = $1 AND enabled = TRUE),
				(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
				 WHERE dim_type = 'model' AND tenant_id = $1 AND bucket >= $2 AND bucket < $3),
				(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
				 WHERE dim_type = 'provider' AND tenant_id = $1 AND bucket >= $2 AND bucket < $3)
		`, tenantID, tr.Start, tr.End).Scan(keys, models, providers)
	}
	return h.db.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM api_keys WHERE enabled = TRUE),
			(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
			 WHERE dim_type = 'model' AND bucket >= $1 AND bucket < $2),
			(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
			 WHERE dim_type = 'provider' AND bucket >= $1 AND bucket < $2)
	`, tr.Start, tr.End).Scan(keys, models, providers)
}

// resolveBoardPies 解析看板饼图，第二个返回值是降级账本。
//
// 2026-10-03：以前只有 (pies, error)。42P01 走日志回退后，回退里每个维度
// 的失败都被吞成空数组，于是这条路径**永远不返回 error** —— 降级事实
// 在函数边界上就丢了，handler 无从知道该不该给页面打标记。
func (h *Handler) resolveBoardPies(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, boardPieDegradation, error) {
	pies, err := h.queryBoardPies(ctx, tenantID, tr)
	if err != nil {
		if IsMissingRelationError(err) {
			return h.fallbackBoardPies(ctx, tenantID, tr)
		}
		return nil, boardPieDegradation{}, err
	}
	// 2026-08-31: only fall back to the slow log-based query when minute
	// stats have NO data for the range. Previously the code also fired
	// the fallback when minute data was present-but-incomplete, which
	// silently made every days>1 board request run 7 sequential pie
	// queries (~9.8s) over request_logs_with_current_month and exhaust
	// the handler's 10s timeout. The minute path is the authoritative
	// source for the operational dashboard; the log path is only a
	// safety net for environments where the minute rollup is missing.
	if piesEmpty(pies) && h.shouldUseBoardLogsFallback(ctx, tenantID, tr) {
		fb, fbDegraded, fbErr := h.fallbackBoardPies(ctx, tenantID, tr)
		if fbErr == nil {
			return fb, fbDegraded, nil
		}
	}
	return pies, boardPieDegradation{}, nil
}

func piesEmpty(pies map[string]any) bool {
	if len(pies) == 0 {
		return true
	}
	for _, v := range pies {
		items, ok := v.([]boardPieItem)
		if ok && len(items) > 0 {
			return false
		}
	}
	return true
}

func (h *Handler) queryBoardPies(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, error) {
	// 2026-09-03 (audit closure): the "clients" pie now aggregates by
	// agent_name (the canonical client-type identifier persisted on
	// request_logs_hot.agent_name) instead of client_profile (the legacy
	// device-fingerprint column).
	types := map[string]string{
		"clients": "agent_name",
		// R57 B7: client_ips 饼图读真源 client_ip 维度（原 virtual_ips 读
		// identity 假名 10.x，GeoIP 归类对它不可达）。响应键同步改名。
		"client_ips":      "client_ip",
		"identity_hashes": "identity_hash",
		"models":          "model",
		"errors":          "error_kind",
		"tenants":         "tenant",
		"providers":       "provider",
	}
	out := make(map[string]any, len(types))
	for key, dimType := range types {
		items, err := h.queryDimPie(ctx, tenantID, tr, dimType)
		if err != nil {
			return nil, err
		}
		if dimType == "provider" {
			items = h.resolveProviderPieLabels(ctx, items)
		}
		// Wave 3 B7 (2026-09-22) + R57 source fix: intranet keeps its raw
		// IP, public IPs collapse to 国家·省·市 through the local segment
		// table, and everything else degrades to the raw IP.
		if dimType == "client_ip" {
			items = classifyClientIPPie(items)
		}
		out[key] = items
	}
	return out, nil
}

func (h *Handler) queryDimPie(ctx context.Context, tenantID string, tr boardTimeRange, dimType string) ([]boardPieItem, error) {
	args := []any{dimType, tr.Start, tr.End}
	where := "dim_type = $1 AND bucket >= $2 AND bucket < $3"
	if tenantID != "" {
		where += " AND tenant_id = $4"
		args = append(args, tenantID)
	}

	rows, err := h.db.Query(ctx, `
		SELECT dim_key,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_stats_dim_minute
		WHERE `+where+`
		GROUP BY dim_key
		ORDER BY SUM(requests) DESC
		LIMIT 25
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []boardPieItem
	for rows.Next() {
		var item boardPieItem
		if err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			warnRowSkip("board pie", err)
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (h *Handler) resolveBoardTrends(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64) ([]boardTrendPoint, error) {
	points, err := h.queryBoardTrends(ctx, tenantID, tr, providerID)
	if err != nil {
		if IsMissingRelationError(err) {
			return h.fallbackBoardTrends(ctx, tenantID, tr, providerID)
		}
		return nil, err
	}
	if trendPointsCoverRange(tr, points) {
		return points, nil
	}
	// 2026-08-31: skip the log fallback when the minute path already has
	// ANY points. The fallback runs GROUP BY date_trunc on 175K rows of
	// request_logs_with_current_month, which takes ~1.4s on days=7 — when
	// added to other fallback work (summary, pies) it exhausts the 10s
	// handler timeout. Returning the partial minute data is the lesser
	// evil: the user sees a recent slice of trends instead of a 10s
	// timeout that returns trends=null and pies via fallback.
	if len(points) > 0 {
		return points, nil
	}
	fallback, fbErr := h.fallbackBoardTrends(ctx, tenantID, tr, providerID)
	if fbErr != nil {
		if len(points) > 0 {
			return points, nil
		}
		return nil, fbErr
	}
	return fallback, nil
}

func (h *Handler) queryBoardTrends(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64) ([]boardTrendPoint, error) {
	where, args := boardMinuteWhere(tr, tenantID, 0)
	if providerID > 0 {
		where += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, providerID)
	}
	bucketExpr := sqlTrendBucket("bucket", tr.trendBucketMinutes())

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_stats_minute
		WHERE %s
		GROUP BY 1
		ORDER BY 1 ASC
	`, bucketExpr, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []boardTrendPoint
	for rows.Next() {
		var p boardTrendPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.Requests, &p.Tokens, &p.Credits, &p.CostUSD); err != nil {
			warnRowSkip("board trends", err)
			continue
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}
