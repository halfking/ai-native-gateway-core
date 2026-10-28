package admin

// topology_models_today.go — 实时拓扑页「模型当天统计」只读投影。
//
//	GET /api/admin/topology/models-today
//
// 2026-10-28（live 页当天汇总轮）：与 providers-today 同表同口径
// （stats_usage_daily，dimension_type='provider_model'）——**不引入第二套
// 真值**。rollup（domains/stats.DailyMonthlyRollup）按 (provider, credential,
// canonical_id, raw_model_name) 逐维落桶；本端点在 canonical_id 上再聚合，
// 同一标准模型跨供应商/凭据/别名合并成一行（与拓扑图模型列的
// canonical 口径一致，否则「图上 5 个模型、栏里 8 行」对不上账）。
//
// 口径与滞后（与 providers-today 完全同款，UI 必须显式展示水位）：
//   - 日桶是 UTC 日；rollup 每小时重建 ⇒ 数据水位与真实时刻可能差 ≤1h；
//   - 响应带 day_utc / source_high_watermark / share（占当日总量比重）。
//
// 租户隔离 / 降级 / 截断语义与 providers-today 一致：statsTenantScope 收敛
// 租户；聚合表缺失 → 200 + degraded 信封；行数上限 + truncated 标注。

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// topologyModelsTodayRow 是一行模型当天聚合（DB 行 → API 行）。
type topologyModelsTodayRow struct {
	CanonicalID      int
	CanonicalName    string
	RawModelName     string
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

type topologyModelTodayItem struct {
	CanonicalID      int     `json:"canonical_id"`
	CanonicalName    string  `json:"canonical_name"`
	RawModelName     string  `json:"raw_model_name"`
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
	Share            float64 `json:"share"`
}

type topologyModelsTodayResponse struct {
	DayUTC              string                  `json:"day_utc"`
	TenantScope         string                  `json:"tenant_scope,omitempty"`
	GeneratedAt         string                  `json:"generated_at"`
	RollupCadence       string                  `json:"rollup_cadence,omitempty"`
	SourceHighWatermark *time.Time              `json:"source_high_watermark,omitempty"`
	Items               []topologyModelTodayItem `json:"items"`
	Truncated           bool                    `json:"truncated"`
	TotalRequests       int64                   `json:"total_requests"`
	Degraded            bool                    `json:"degraded"`
	ErrorCode           string                  `json:"error_code,omitempty"`
}

// modelsTodayLimit 与 providersTodayLimit 同理：几十~几百量级，500 是
// 「足够全 + 挡异常膨胀」的双保险；截断必须显式标注。
const modelsTodayLimit = 500

// modelsTodaySQL：canonical_id 上聚合（canonical 名 LEFT JOIN models_canonical
// 解析；canonical_id=0 的 passthrough 行回落 raw_model_name 当名字——
// 与拓扑图 modelKeyOf「canonical 优先、原始名兜底」同一口径）。
const modelsTodaySQL = `
WITH agg AS (
	SELECT canonical_id,
	       COALESCE(MAX(NULLIF(raw_model_name, '')), '') AS raw_model_name,
	       SUM(request_count)::bigint        AS requests,
	       SUM(success_count)::bigint        AS success,
	       SUM(failure_count)::bigint        AS failure,
	       SUM(rate_limited_count)::bigint   AS rate_limited,
	       COALESCE(SUM(prompt_tokens),0)::bigint      AS prompt_tokens,
	       COALESCE(SUM(completion_tokens),0)::bigint  AS completion_tokens,
	       COALESCE(SUM(cache_read_tokens),0)::bigint  AS cache_read_tokens,
	       COALESCE(SUM(cache_write_tokens),0)::bigint AS cache_write_tokens,
	       COALESCE(SUM(total_tokens),0)::bigint       AS total_tokens,
	       COALESCE(SUM(credits_charged),0)::bigint    AS credits_charged,
	       COALESCE(SUM(cost_usd),0)::float8           AS cost_usd,
	       MAX(source_max_occurred_at)                  AS watermark
	FROM stats_usage_daily
	WHERE day_utc = $1::date AND dimension_type = 'provider_model'
	  AND ($2::text = '' OR tenant_id = $2)
	GROUP BY canonical_id
)
SELECT a.canonical_id,
       COALESCE(NULLIF(mc.canonical_name, ''), ''),
       COALESCE(a.raw_model_name, ''),
       a.requests, a.success, a.failure, a.rate_limited,
       a.prompt_tokens, a.completion_tokens,
       a.cache_read_tokens, a.cache_write_tokens, a.total_tokens,
       a.credits_charged, a.cost_usd, a.watermark
FROM agg a
LEFT JOIN models_canonical mc ON mc.id = a.canonical_id
ORDER BY a.requests DESC, a.canonical_id
LIMIT $3`

// handleTopologyModelsToday 服务实时拓扑「当天模型请求/token/缓存汇总」。
func (h *Handler) handleTopologyModelsToday(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant := statsTenantScope(r)
	day := todayUTCDay(time.Now())

	querier := h.topologyStatsQuerier()
	if querier == nil {
		writeJSON(w, http.StatusOK, topologyModelsTodayResponse{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			DayUTC:      day.Format("2006-01-02"),
			Items:       []topologyModelTodayItem{},
			Degraded:    true,
			ErrorCode:   "TOPOLOGY_STATS_UNAVAILABLE",
		})
		return
	}

	limit := modelsTodayLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= modelsTodayLimit {
		limit = n
	}

	rows, err := querier.Query(r.Context(), modelsTodaySQL, day, strings.TrimSpace(tenant), limit)
	if err != nil {
		if isMissingStatsRelation(err) {
			writeJSON(w, http.StatusOK, topologyModelsTodayResponse{
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
				DayUTC:      day.Format("2006-01-02"),
				Items:       []topologyModelTodayItem{},
				Degraded:    true,
				ErrorCode:   "STATS_TABLE_MISSING",
			})
			return
		}
		writeJSON(w, http.StatusOK, topologyModelsTodayResponse{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			DayUTC:      day.Format("2006-01-02"),
			Items:       []topologyModelTodayItem{},
			Degraded:    true,
			ErrorCode:   "QUERY_FAILED",
		})
		return
	}
	defer rows.Close()

	items := make([]topologyModelTodayItem, 0, 16)
	var totalRequests int64
	// 水位取各行 watermark 的最大值（与 providers-today 同款：真实数据水位，
	// 不是响应生成时刻——用 now() 冒充水位会把 rollup 滞后藏起来）。
	var watermark *time.Time
	for rows.Next() {
		var row topologyModelsTodayRow
		if err := rows.Scan(
			&row.CanonicalID, &row.CanonicalName, &row.RawModelName,
			&row.Requests, &row.Success, &row.Failure, &row.RateLimited,
			&row.PromptTokens, &row.CompletionTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &row.TotalTokens,
			&row.CreditsCharged, &row.CostUSD, &row.Watermark,
		); err != nil {
			// 坏行跳过并计数（与 providers-today 的 warnRowSkip 同款）：
			// 一行形状意外不该把整个端点打成 degraded。
			warnRowSkip("topology.models_today", err)
			continue
		}
		if row.Watermark.Valid && (watermark == nil || row.Watermark.Time.After(*watermark)) {
			ts := row.Watermark.Time
			watermark = &ts
		}
		items = append(items, topologyModelTodayItem{
			CanonicalID:      row.CanonicalID,
			CanonicalName:    row.CanonicalName,
			RawModelName:     row.RawModelName,
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
		totalRequests += row.Requests
	}
	if err := rows.Err(); err != nil {
		if !isMissingStatsRelation(err) {
			writeError(w, http.StatusInternalServerError, "topology models-today rows failed")
			return
		}
	}

	// share：分母用全量 requests（截断后可见行的占比仍是「当日总量里的比重」，
	// 与 providers-today 的 share 语义一致）。
	for i := range items {
		if totalRequests > 0 {
			items[i].Share = float64(items[i].Requests) / float64(totalRequests)
		}
	}

	writeJSON(w, http.StatusOK, topologyModelsTodayResponse{
		DayUTC:              day.Format("2006-01-02"),
		TenantScope:         tenant,
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		RollupCadence:       "1h",
		SourceHighWatermark: watermark,
		Items:               items,
		Truncated:           len(items) >= limit,
		TotalRequests:       totalRequests,
	})
}
