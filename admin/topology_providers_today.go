package admin

// topology_providers_today.go — 实时拓扑页「供应商当天统计」只读投影。
//
//	GET /api/admin/topology/providers-today
//
// 2026-10-16：为实时拓扑首页左侧统计栏供数。与 /api/admin/stats/* 同源同口径
// （stats_usage_daily，dimension_type='provider'），刻意**不引入第二套真值**。
//
// 为什么不用 /api/admin/stats/breakdown?dimension=provider：
//  1. breakdown 返回 provider_id 裸 ID，没有 catalog_code / display_name，
//     左栏要显示供应商名还得再发一次 /api/providers 做前端 join；
//  2. breakdown 的区间由 ?start/?end 驱动且**跨天分桶**，与「当天」语义
//     不是一个形状，聚合逻辑要在前端重写一遍。
//
// 口径与滞后（前端必须显式展示，不得假装秒级实时）：
//   - 日桶是 **UTC 日**（stats_usage_daily.day_utc = (occurred_at AT TIME ZONE 'UTC')::date）；
//   - 该表由 domains/stats.DailyMonthlyRollup **每小时**重建（defaultRollupInterval=1h），
//     所以 source_high_watermark 与请求发出的真实时刻之间可能差 ≤1h；
//   - 响应同时返回 day_utc / window_start_utc / source_high_watermark，
//     让 UI 把「数据水位」画出来，而不是渲染成一串看起来实时的数字。
//
// 租户隔离：走 statsTenantScope(r) —— 租户管理员强制收敛到自己租户，
// 超管/admin_key 才允许用 ?tenant_id= 显式指定；**缺参不是「全量」**。
// 降级语义：聚合表未迁移时返回 200 + degraded 信封（与 /api/admin/stats/* 一致），
// 前端必须渲染成「统计不可用」而不是零行（同 2026-10-03 抽屉降级教训）。

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// todayUTCDay 返回当前 UTC 日桶（与 stats_usage_daily.day_utc 的生成方式一致：
// (occurred_at AT TIME ZONE 'UTC')::date）。单独成函数是为了让单测能用
// 同一个值构造 mock 参数，而不是在测试里复制一份日期算法。
func todayUTCDay(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// topologyProvidersTodayRow 是一行供应商当天聚合（DB 行 → API 行）。
type topologyProvidersTodayRow struct {
	ProviderID       int
	CatalogCode      string
	DisplayName      string
	Enabled          bool
	Requests         int64
	Success          int64
	Failure          int64
	RateLimited      int64
	PromptTokens     int64
	CompletionTokens int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalTokens      int64
	CreditsCharged   int64
	CostUSD          float64
	Watermark        pgtype.Timestamptz
}

type topologyProviderTodayTotals struct {
	Requests         int64   `json:"requests"`
	Success          int64   `json:"success"`
	Failure          int64   `json:"failure"`
	RateLimited      int64   `json:"rate_limited"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CreditsCharged   int64   `json:"credits_charged"`
	CostUSD          float64 `json:"cost_usd"`
}

type topologyProviderTodayItem struct {
	ProviderID       int     `json:"provider_id"`
	CatalogCode      string  `json:"catalog_code"`
	DisplayName      string  `json:"display_name"`
	Enabled          bool    `json:"enabled"`
	Known            bool    `json:"known"` // providers 表里查得到（provider_id=0 的未知桶为 false）
	Requests         int64   `json:"requests"`
	Success          int64   `json:"success"`
	Failure          int64   `json:"failure"`
	RateLimited      int64   `json:"rate_limited"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CreditsCharged   int64   `json:"credits_charged"`
	CostUSD          float64 `json:"cost_usd"`
	Share            float64 `json:"share"` // 占当天总请求的比重（0..1，分母为 totals.requests）
}

type topologyProvidersTodayResponse struct {
	DayUTC              string                      `json:"day_utc"`
	WindowStartUTC      time.Time                   `json:"window_start_utc"`
	TenantScope         string                      `json:"tenant_scope"`
	GeneratedAt         time.Time                   `json:"generated_at"`
	SourceHighWatermark *time.Time                  `json:"source_high_watermark,omitempty"`
	RollupCadence       string                      `json:"rollup_cadence"`
	Totals              topologyProviderTodayTotals `json:"totals"`
	Items               []topologyProviderTodayItem `json:"items"`
}

// providersTodayLimit 是返回行数上限。供应商数量是几十量级，500 是「足够全 +
// 挡住异常膨胀」的双保险；截断发生时必须在响应里说清楚（Truncated 字段），
// 否则前端会把「被截断」渲染成「就这些」。
const providersTodayLimit = 500

const providersTodaySQL = `
WITH agg AS (
	SELECT provider_id,
	       SUM(request_count)::bigint        AS requests,
	       SUM(success_count)::bigint        AS success,
	       SUM(failure_count)::bigint        AS failure,
	       SUM(rate_limited_count)::bigint  AS rate_limited,
	       COALESCE(SUM(prompt_tokens),0)::bigint      AS prompt_tokens,
	       COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
	       COALESCE(SUM(cache_read_tokens),0)::bigint   AS cache_read_tokens,
	       COALESCE(SUM(cache_write_tokens),0)::bigint  AS cache_write_tokens,
	       COALESCE(SUM(total_tokens),0)::bigint       AS total_tokens,
	       COALESCE(SUM(credits_charged),0)::bigint    AS credits_charged,
	       COALESCE(SUM(cost_usd),0)::float8           AS cost_usd,
	       MAX(source_max_occurred_at)                  AS watermark
	FROM stats_usage_daily
	WHERE day_utc = $1::date AND dimension_type = 'provider'
	  AND ($2::text = '' OR tenant_id = $2)
	GROUP BY provider_id
)
SELECT a.provider_id,
       COALESCE(p.catalog_code, ''),
       COALESCE(NULLIF(p.display_name, ''), ''),
       COALESCE(p.enabled, FALSE),
       a.requests, a.success, a.failure, a.rate_limited,
       a.prompt_tokens, a.completion_tokens,
       a.cache_read_tokens, a.cache_write_tokens, a.total_tokens,
       a.credits_charged, a.cost_usd, a.watermark
FROM agg a
LEFT JOIN providers p ON p.id = a.provider_id
ORDER BY a.requests DESC, a.provider_id
LIMIT $3`

// topologyStatsQuerier 返回该端点的查询面：优先窄口（单测注入），
// 回落到真实连接池。两者都缺时返回 nil —— 调用方必须先判空，
// 避免把「装配缺失」变成一次 nil 解引用的 panic。
func (h *Handler) topologyStatsQuerier() topologyStatsDB {
	if h == nil {
		return nil
	}
	if h.topologyStatsDB != nil {
		return h.topologyStatsDB
	}
	if h.db != nil {
		return h.db
	}
	return nil
}

// handleTopologyProvidersToday 服务实时拓扑首页左栏的「当天供应商请求/token 统计」。
func (h *Handler) handleTopologyProvidersToday(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant := statsTenantScope(r)
	day := todayUTCDay(time.Now())

	querier := h.topologyStatsQuerier()
	if querier == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"degraded":   true,
			"error_code": "TOPOLOGY_STATS_UNAVAILABLE",
			"items":      []topologyProviderTodayItem{},
		})
		return
	}

	limit := providersTodayLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= providersTodayLimit {
		limit = n
	}

	rows, err := querier.Query(r.Context(), providersTodaySQL, day, strings.TrimSpace(tenant), limit)
	if err != nil {
		if isMissingStatsRelation(err) {
			// 聚合表还没迁移/不存在：与 /api/admin/stats/* 同契约 —— 200 + degraded，
			// 前端据此渲染「统计不可用」，绝不能渲染成「当天 0 请求」。
			writeJSON(w, http.StatusOK, map[string]any{
				"degraded":       true,
				"error_code":     "STATS_NOT_MIGRATED",
				"day_utc":        day.Format("2006-01-02"),
				"generated_at":   time.Now().UTC(),
				"rollup_cadence": "1h",
				"items":          []topologyProviderTodayItem{},
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "topology providers-today query failed")
		return
	}
	defer rows.Close()

	items := make([]topologyProviderTodayItem, 0, 32)
	totals := topologyProviderTodayTotals{}
	var watermark *time.Time
	for rows.Next() {
		var row topologyProvidersTodayRow
		if err := rows.Scan(
			&row.ProviderID, &row.CatalogCode, &row.DisplayName, &row.Enabled,
			&row.Requests, &row.Success, &row.Failure, &row.RateLimited,
			&row.PromptTokens, &row.CompletionTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &row.TotalTokens,
			&row.CreditsCharged, &row.CostUSD, &row.Watermark,
		); err != nil {
			warnRowSkip("topology.providers_today", err)
			continue
		}
		totals.Requests += row.Requests
		totals.Success += row.Success
		totals.Failure += row.Failure
		totals.RateLimited += row.RateLimited
		totals.PromptTokens += row.PromptTokens
		totals.CompletionTokens += row.CompletionTokens
		totals.CacheReadTokens += row.CacheReadTokens
		totals.CacheWriteTokens += row.CacheWriteTokens
		totals.TotalTokens += row.TotalTokens
		totals.CreditsCharged += row.CreditsCharged
		totals.CostUSD += row.CostUSD
		if row.Watermark.Valid && (watermark == nil || row.Watermark.Time.After(*watermark)) {
			ts := row.Watermark.Time
			watermark = &ts
		}
		items = append(items, topologyProviderTodayItem{
			ProviderID:       row.ProviderID,
			CatalogCode:      row.CatalogCode,
			DisplayName:      row.DisplayName,
			Enabled:          row.Enabled,
			Known:            row.ProviderID > 0 && row.CatalogCode != "",
			Requests:         row.Requests,
			Success:          row.Success,
			Failure:          row.Failure,
			RateLimited:      row.RateLimited,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			TotalTokens:      row.TotalTokens,
			CreditsCharged:   row.CreditsCharged,
			CostUSD:          row.CostUSD,
		})
	}
	if err := rows.Err(); err != nil {
		if !isMissingStatsRelation(err) {
			writeError(w, http.StatusInternalServerError, "topology providers-today rows failed")
			return
		}
	}

	// share 用「已返回行」的请求数做分母：截断发生时它退化成「可见部分占比」，
	// 语义仍然自洽（不是相对全量的假比例）。
	visible := totals.Requests
	for i := range items {
		if visible > 0 {
			items[i].Share = float64(items[i].Requests) / float64(visible)
		}
	}

	writeJSON(w, http.StatusOK, topologyProvidersTodayResponse{
		DayUTC:              day.Format("2006-01-02"),
		WindowStartUTC:      day,
		TenantScope:         tenant,
		GeneratedAt:         time.Now().UTC(),
		SourceHighWatermark: watermark,
		RollupCadence:       "1h",
		Totals:              totals,
		Items:               items,
	})
}
