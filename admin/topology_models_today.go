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
	// Providers 仅在 ?by=provider 时填充（交叉展开：该模型在哪些供应商上
	// 各跑了多少）。omitempty：默认调用的响应形状与既有契约逐字节兼容。
	Providers []topologyModelProviderItem `json:"providers,omitempty"`
}

type topologyModelsTodayResponse struct {
	DayUTC              string                   `json:"day_utc"`
	TenantScope         string                   `json:"tenant_scope,omitempty"`
	GeneratedAt         string                   `json:"generated_at"`
	RollupCadence       string                   `json:"rollup_cadence,omitempty"`
	SourceHighWatermark *time.Time               `json:"source_high_watermark,omitempty"`
	Items               []topologyModelTodayItem `json:"items"`
	Truncated           bool                     `json:"truncated"`
	TotalRequests       int64                    `json:"total_requests"`
	Degraded            bool                     `json:"degraded"`
	ErrorCode           string                   `json:"error_code,omitempty"`
}

// topologyModelProviderItem 是 ?by=provider 展开后的子行：一个模型在一个
// 供应商上的当天量。Share 的分母是**该模型的 requests**（模型内分布），
// 与父行的 share（当日总量占比）不同分母，字段名相同语义分层——前端
// 按「占比」渲染子行时展示的是模型内分布。
type topologyModelProviderItem struct {
	ProviderID       int     `json:"provider_id"`
	CatalogCode      string  `json:"catalog_code"`
	DisplayName      string  `json:"display_name"`
	Known            bool    `json:"known"`
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

// row 是 by=provider 折叠路径的 DB 行（比默认行多 provider 三列）。
type topologyModelsTodayProviderRow struct {
	CanonicalID      int
	CanonicalName    string
	RawModelName     string
	ProviderID       int
	CatalogCode      string
	DisplayName      string
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

// modelsTodayByProviderSQL：?by=provider 交叉展开（2026-10-28 遗留①）。
// provider_model 维度原生带 provider_id（rollup 按 (provider, credential,
// canonical, raw_model) 落桶）⇒ 在 (canonical_id, provider_id) 上聚合，先取
// 请求量 top-N 的 canonical（与默认口径同一截断语义），再返回每个模型的
// 逐供应商行。Go 侧把同一模型的行折叠成父行 + providers 子行——父行指标
// 由子行求和而来（同一 WHERE/GROUP BY，无 join 丢失），「父 = Σ子」由
// 构造保证，测试断言这条不变量。
const modelsTodayByProviderSQL = `
WITH agg AS (
	SELECT canonical_id,
	       provider_id,
	       MAX(NULLIF(raw_model_name, ''))         AS raw_model_name,
	       SUM(request_count)::bigint              AS requests,
	       SUM(success_count)::bigint              AS success,
	       SUM(failure_count)::bigint              AS failure,
	       SUM(rate_limited_count)::bigint         AS rate_limited,
	       COALESCE(SUM(prompt_tokens),0)::bigint      AS prompt_tokens,
	       COALESCE(SUM(completion_tokens),0)::bigint  AS completion_tokens,
	       COALESCE(SUM(cache_read_tokens),0)::bigint   AS cache_read_tokens,
	       COALESCE(SUM(cache_write_tokens),0)::bigint  AS cache_write_tokens,
	       COALESCE(SUM(total_tokens),0)::bigint       AS total_tokens,
	       COALESCE(SUM(credits_charged),0)::bigint    AS credits_charged,
	       COALESCE(SUM(cost_usd),0)::float8           AS cost_usd,
	       MAX(source_max_occurred_at)                  AS watermark
	FROM stats_usage_daily
	WHERE day_utc = $1::date AND dimension_type = 'provider_model'
	  AND ($2::text = '' OR tenant_id = $2)
	GROUP BY canonical_id, provider_id
),
models AS (
	SELECT canonical_id,
	       SUM(requests)::bigint AS model_requests,
	       MAX(raw_model_name)   AS raw_model_name
	FROM agg
	GROUP BY canonical_id
),
kept AS (
	SELECT canonical_id, model_requests
	FROM models
	ORDER BY model_requests DESC, canonical_id
	LIMIT $3
)
SELECT a.canonical_id,
       COALESCE(NULLIF(mc.canonical_name, ''), ''),
       COALESCE(m.raw_model_name, ''),
       a.provider_id,
       COALESCE(p.catalog_code, ''),
       COALESCE(NULLIF(p.display_name, ''), ''),
       a.requests, a.success, a.failure, a.rate_limited,
       a.prompt_tokens, a.completion_tokens,
       a.cache_read_tokens, a.cache_write_tokens, a.total_tokens,
       a.credits_charged, a.cost_usd, a.watermark
FROM agg a
JOIN kept k ON k.canonical_id = a.canonical_id
JOIN models m ON m.canonical_id = a.canonical_id
LEFT JOIN models_canonical mc ON mc.id = a.canonical_id
LEFT JOIN providers p ON p.id = a.provider_id
ORDER BY k.model_requests DESC, a.canonical_id, a.requests DESC, a.provider_id`

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

	// ?by=provider：交叉展开路径（canonical × provider 二元组，Go 侧折叠）。
	if strings.TrimSpace(r.URL.Query().Get("by")) == "provider" {
		h.handleTopologyModelsTodayByProvider(w, r, querier, day, tenant, limit)
		return
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

// handleTopologyModelsTodayByProvider 是 ?by=provider 的折叠实现：
// DB 返回 (canonical_id, provider_id) 二元组行，同一 canonical 的行折叠成
// 一个父 item + Providers 子行。父行指标 = Σ 子行（同一 WHERE/GROUP BY，
// LEFT JOIN providers 不丢 provider_id=0 的未知桶行）——「父 = Σ子」由
// 构造保证，测试断言这条不变量，防止将来有人在父行再算一套。
//
// 异常路径与默认口径同款（§507 通式六的五件套）：同 tenant scope、同
// 降级信封、同水位取法、同坏行跳过、同截断标注（截断按 canonical 行数
// 判，与默认口径一致）。
func (h *Handler) handleTopologyModelsTodayByProvider(
	w http.ResponseWriter, r *http.Request, querier topologyStatsDB,
	day time.Time, tenant string, limit int,
) {
	rows, err := querier.Query(r.Context(), modelsTodayByProviderSQL, day, strings.TrimSpace(tenant), limit)
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
	index := make(map[int]int, 16) // canonical_id → items 下标
	var totalRequests int64
	var watermark *time.Time
	for rows.Next() {
		var row topologyModelsTodayProviderRow
		if err := rows.Scan(
			&row.CanonicalID, &row.CanonicalName, &row.RawModelName,
			&row.ProviderID, &row.CatalogCode, &row.DisplayName,
			&row.Requests, &row.Success, &row.Failure, &row.RateLimited,
			&row.PromptTokens, &row.CompletionTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &row.TotalTokens,
			&row.CreditsCharged, &row.CostUSD, &row.Watermark,
		); err != nil {
			// 坏行跳过并计数（与默认口径 warnRowSkip 同款）：一行形状意外
			// 不该把整个端点打成 degraded。
			warnRowSkip("topology.models_today_by_provider", err)
			continue
		}
		if row.Watermark.Valid && (watermark == nil || row.Watermark.Time.After(*watermark)) {
			ts := row.Watermark.Time
			watermark = &ts
		}
		at, ok := index[row.CanonicalID]
		if !ok {
			items = append(items, topologyModelTodayItem{
				CanonicalID:   row.CanonicalID,
				CanonicalName: row.CanonicalName,
				RawModelName:  row.RawModelName,
				Providers:     make([]topologyModelProviderItem, 0, 4),
			})
			at = len(items) - 1
			index[row.CanonicalID] = at
		}
		parent := &items[at]
		// 父行指标 = Σ 子行（构造保证的守恒，不另算一份）。
		parent.Requests += row.Requests
		parent.Success += row.Success
		parent.Failure += row.Failure
		parent.RateLimited += row.RateLimited
		parent.PromptTokens += row.PromptTokens
		parent.CompletionTokens += row.CompletionTokens
		parent.CacheReadTokens += row.CacheReadTokens
		parent.CacheWriteTokens += row.CacheWriteTokens
		parent.TotalTokens += row.TotalTokens
		parent.CreditsCharged += row.CreditsCharged
		parent.CostUSD += row.CostUSD
		parent.Providers = append(parent.Providers, topologyModelProviderItem{
			ProviderID:       row.ProviderID,
			CatalogCode:      row.CatalogCode,
			DisplayName:      row.DisplayName,
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
			writeError(w, http.StatusInternalServerError, "topology models-today rows failed")
			return
		}
	}

	for i := range items {
		totalRequests += items[i].Requests
	}
	// 两层 share 分层：父行 share = 当日总量占比（与默认口径同分母）；
	// 子行 share = 该模型的 requests 内分布（「这个模型主要跑在谁家」）。
	for i := range items {
		if totalRequests > 0 {
			items[i].Share = float64(items[i].Requests) / float64(totalRequests)
		}
		for j := range items[i].Providers {
			if items[i].Requests > 0 {
				items[i].Providers[j].Share = float64(items[i].Providers[j].Requests) / float64(items[i].Requests)
			}
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
